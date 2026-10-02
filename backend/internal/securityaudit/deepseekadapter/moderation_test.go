package deepseekadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func tinyPNGDataURL() string {
	return "data:image/png;base64," + tinyPNGBase64
}

const tinyWebPBase64 = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="

func moderationImageFixture(t *testing.T, format string) []byte {
	t.Helper()
	if format == "webp" {
		raw, err := base64.StdEncoding.DecodeString(tinyWebPBase64)
		require.NoError(t, err)
		return raw
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	case "png":
		err = png.Encode(&buf, img)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	default:
		t.Fatalf("unsupported fixture format %q", format)
	}
	require.NoError(t, err)
	return buf.Bytes()
}

func truncatedModerationImageFixture(t *testing.T, format string, raw []byte) []byte {
	t.Helper()
	for cut := len(raw) - 1; cut > 0; cut-- {
		candidate := raw[:cut]
		_, decodedFormat, configErr := image.DecodeConfig(bytes.NewReader(candidate))
		if configErr != nil || decodedFormat != format {
			continue
		}
		if _, canonicalErr := canonicalizeModerationImageBytes(candidate); canonicalErr != nil {
			return append([]byte(nil), candidate...)
		}
	}
	t.Fatalf("could not construct header-valid truncated %s fixture", format)
	return nil
}

func TestModerationVisionSupportsAllDocumentedImageFormats(t *testing.T) {
	mimes := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp"}
	for format, wantMIME := range mimes {
		raw := moderationImageFixture(t, format)
		for _, source := range []string{"inline", "remote"} {
			t.Run(format+"/"+source, func(t *testing.T) {
				var forwardedURL string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					messages, ok := body["messages"].([]any)
					require.True(t, ok)
					require.Len(t, messages, 2)
					message, ok := messages[1].(map[string]any)
					require.True(t, ok)
					content, ok := message["content"].([]any)
					require.True(t, ok)
					for _, item := range content {
						part, ok := item.(map[string]any)
						require.True(t, ok)
						if part["type"] == "image_url" {
							imageURL, ok := part["image_url"].(map[string]any)
							require.True(t, ok)
							forwardedURL, ok = imageURL["url"].(string)
							require.True(t, ok)
						}
					}
					writeJSON(w, http.StatusOK, map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": moderationUpstreamContent("Safe")}}}})
				}))
				defer upstream.Close()

				var imageServer *httptest.Server
				sourceURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
				if source == "remote" {
					imageServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/octet-stream")
						_, _ = w.Write(raw)
					}))
					defer imageServer.Close()
					sourceURL = imageServer.URL + "/image"
				}
				config := testConfig(upstream.URL)
				config.Model = "deepseek-v4-flash-vision-exp"
				handler, cleanup, err := NewHandler(config)
				require.NoError(t, err)
				defer cleanup()
				payload, err := json.Marshal(map[string]any{"input": []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": sourceURL}}}})
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations", bytes.NewReader(payload)))
				require.Equal(t, http.StatusOK, recorder.Code)
				require.True(t, strings.HasPrefix(forwardedURL, "data:"+wantMIME+";base64,"), forwardedURL)
			})
		}
	}
}

func TestModerationVisionRejectsHeaderValidTruncatedImages(t *testing.T) {
	for _, format := range []string{"jpeg", "png", "gif", "webp"} {
		raw := moderationImageFixture(t, format)
		truncated := truncatedModerationImageFixture(t, format, raw)
		for _, source := range []string{"inline", "remote"} {
			t.Run(format+"/"+source, func(t *testing.T) {
				var classificationRequests atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					classificationRequests.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer upstream.Close()
				sourceURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(truncated)
				var imageServer *httptest.Server
				if source == "remote" {
					imageServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						_, _ = w.Write(truncated)
					}))
					defer imageServer.Close()
					sourceURL = imageServer.URL + "/image"
				}
				config := testConfig(upstream.URL)
				config.Model = "deepseek-v4-flash-vision-exp"
				handler, cleanup, err := NewHandler(config)
				require.NoError(t, err)
				defer cleanup()
				payload, err := json.Marshal(map[string]any{"input": []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": sourceURL}}}})
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations", bytes.NewReader(payload)))
				require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
				require.Zero(t, classificationRequests.Load())
			})
		}
	}
}
func moderationUpstreamContent(safety string, categories ...string) string {
	if categories == nil {
		categories = []string{}
	}
	raw, _ := json.Marshal(map[string]any{"safety": safety, "categories": categories})
	return string(raw)
}

func TestModerationsEndpointReturnsOpenAICompatibleScores(t *testing.T) {
	var seen map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer deepseek-test-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Unsafe", "illicit"),
			}}},
		})
	}))
	defer upstream.Close()

	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader(`{"model":"deepseek-v4-flash","input":"explain the supplied request"}`)))

	require.Equal(t, http.StatusOK, recorder.Code)
	var response moderationResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "deepseek-v4-flash", response.Model)
	require.Len(t, response.Results, 1)
	require.True(t, response.Results[0].Flagged)
	require.True(t, response.Results[0].Categories["illicit"])
	require.Equal(t, 1.0, response.Results[0].CategoryScores["illicit"])
	require.Len(t, response.Results[0].CategoryScores, len(moderationCategoryOrder))

	require.Equal(t, "deepseek-v4-flash", seen["model"])
	require.Equal(t, map[string]any{"type": "disabled"}, seen["thinking"])
	messages, ok := seen["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	systemMessage, ok := messages[0].(map[string]any)
	require.True(t, ok)
	userMessage, ok := messages[1].(map[string]any)
	require.True(t, ok)
	require.Contains(t, systemMessage["content"], "Benign discussion")
	require.Contains(t, systemMessage["content"], `"safety":"Safe|Controversial|Unsafe"`)
	require.NotContains(t, systemMessage["content"], "category_scores")
	require.Contains(t, userMessage["content"], "<BEGIN_UNTRUSTED_TEXT>")
}

func TestModerationsEndpointSupportsStringArrayAndTextParts(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Safe"),
			}}},
		})
	}))
	defer upstream.Close()
	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	batch := httptest.NewRecorder()
	handler.ServeHTTP(batch, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader(`{"input":["first","second"]}`)))
	require.Equal(t, http.StatusOK, batch.Code)
	var batchResponse moderationResponse
	require.NoError(t, json.Unmarshal(batch.Body.Bytes(), &batchResponse))
	require.Len(t, batchResponse.Results, 2)
	require.False(t, batchResponse.Results[0].Flagged)

	parts := httptest.NewRecorder()
	handler.ServeHTTP(parts, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader(`{"input":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`)))
	require.Equal(t, http.StatusOK, parts.Code)
	var partsResponse moderationResponse
	require.NoError(t, json.Unmarshal(parts.Body.Bytes(), &partsResponse))
	require.Len(t, partsResponse.Results, 1)
	require.Equal(t, int32(3), requests.Load())
}

func TestModerationsEndpointRejectsImagesWithoutCallingUpstream(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader(`{"input":[{"type":"text","text":"caption"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}`)))
	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	require.Equal(t, int32(0), requests.Load())
}

func TestModerationsEndpointVisionSupportsInlineAndRemoteImages(t *testing.T) {
	imageBytes, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	require.NoError(t, err)
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer imageServer.Close()

	var upstreamBodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		upstreamBodies = append(upstreamBodies, body)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Safe"),
			}}},
		})
	}))
	defer upstream.Close()

	config := testConfig(upstream.URL)
	config.Model = "deepseek-v4-flash-vision-exp"
	handler, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	defer cleanup()

	inlineBody, err := json.Marshal(map[string]any{
		"model": config.Model,
		"input": []any{
			map[string]any{"type": "text", "text": "caption"},
			map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": tinyPNGDataURL(), "detail": "original",
			}},
		},
	})
	require.NoError(t, err)
	inline := httptest.NewRecorder()
	handler.ServeHTTP(inline, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(string(inlineBody))))
	require.Equal(t, http.StatusOK, inline.Code)

	remoteBody, err := json.Marshal(map[string]any{
		"model": config.Model,
		"input": []any{
			map[string]any{"type": "text", "text": "remote caption"},
			map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": imageServer.URL + "/image.png", "detail": "low",
			}},
		},
	})
	require.NoError(t, err)
	remote := httptest.NewRecorder()
	handler.ServeHTTP(remote, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(string(remoteBody))))
	require.Equal(t, http.StatusOK, remote.Code)

	require.Len(t, upstreamBodies, 2)
	require.Equal(t, config.Model, upstreamBodies[0]["model"])
	extractImage := func(body map[string]any) map[string]any {
		t.Helper()
		messages, ok := body["messages"].([]any)
		require.True(t, ok)
		require.GreaterOrEqual(t, len(messages), 2)
		message, ok := messages[1].(map[string]any)
		require.True(t, ok)
		content, ok := message["content"].([]any)
		require.True(t, ok)
		require.GreaterOrEqual(t, len(content), 2)
		imagePart, ok := content[1].(map[string]any)
		require.True(t, ok)
		image, ok := imagePart["image_url"].(map[string]any)
		require.True(t, ok)
		return image
	}
	inlineImage := extractImage(upstreamBodies[0])
	require.Equal(t, tinyPNGDataURL(), inlineImage["url"])
	require.Equal(t, "original", inlineImage["detail"])

	remotePayload, err := json.Marshal(upstreamBodies[1])
	require.NoError(t, err)
	require.NotContains(t, string(remotePayload), imageServer.URL)
	remoteImage := extractImage(upstreamBodies[1])
	remoteURL, ok := remoteImage["url"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(remoteURL, "data:image/png;base64,"))
	require.Equal(t, "low", remoteImage["detail"])
}

func TestModerationsEndpointVisionRejectsInvalidImagesWithoutCallingUpstream(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Safe"),
			}}},
		})
	}))
	defer upstream.Close()

	imageBytes, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	require.NoError(t, err)
	var redirectTargetRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetRequests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBytes)
	}))
	defer redirectTarget.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	config := testConfig(upstream.URL)
	config.Model = "deepseek-v4-flash-vision-exp"
	handler, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	defer cleanup()

	cases := []string{
		`{"input":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW52YWxpZA=="}}]}`,
		`{"input":[{"type":"image_url","image_url":{"url":"data:image/png;base64,%%%"}}]}`,
		`{"input":[{"type":"image_url","image_url":{"url":"` + redirect.URL + `"}}]}`,
		`{"input":[{"type":"image_url","image_url":{"url":"` + tinyPNGDataURL() + `"}},{"type":"image_url","image_url":{"url":"` + tinyPNGDataURL() + `"}}]}`,
	}
	for _, body := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(body)))
		require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
		var response map[string]map[string]string
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		require.Equal(t, "unsupported_input", response["error"]["code"])
	}
	require.Zero(t, redirectTargetRequests.Load(), "redirect target must not be fetched")
	require.Zero(t, upstreamRequests.Load())
}

func TestModerationsEndpointVisionRejectsImageResourceLimitsWithoutCallingUpstream(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Safe"),
			}}},
		})
	}))
	defer upstream.Close()

	oversizedRemote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxModerationImageBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer oversizedRemote.Close()

	dimensionBytes := bytes.Buffer{}
	dimensionImage := image.NewRGBA(image.Rect(0, 0, maxModerationImageDimension+1, 1))
	require.NoError(t, png.Encode(&dimensionBytes, dimensionImage))
	dimensionRemote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(dimensionBytes.Bytes())
	}))
	defer dimensionRemote.Close()

	config := testConfig(upstream.URL)
	config.Model = "deepseek-v4-flash-vision-exp"
	handler, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	defer cleanup()

	dataURLTooLarge := `{"input":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("A", maxModerationImageDataURLBytes) + `"}}]}`
	requestTooLarge := `{"input":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("A", maxModerationRequestBytes) + `"}}]}`
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "data URL limit", body: dataURLTooLarge, wantStatus: http.StatusUnprocessableEntity},
		{name: "request limit", body: requestTooLarge, wantStatus: http.StatusBadRequest},
		{name: "remote byte limit", body: `{"input":[{"type":"image_url","image_url":{"url":"` + oversizedRemote.URL + `/image.png"}}]}`, wantStatus: http.StatusUnprocessableEntity},
		{name: "dimension limit", body: `{"input":[{"type":"image_url","image_url":{"url":"` + dimensionRemote.URL + `/image.png"}}]}`, wantStatus: http.StatusUnprocessableEntity},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(test.body)))
			require.Equal(t, test.wantStatus, recorder.Code)
			if test.wantStatus == http.StatusUnprocessableEntity {
				var response map[string]map[string]string
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
				require.Equal(t, "unsupported_input", response["error"]["code"])
			}
		})
	}
	require.Zero(t, upstreamRequests.Load())
}

func TestModerationImageProductionRejectsPrivateIP(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "100.100.100.200", "192.0.2.1", "2001:db8::1"} {
		t.Run(address, func(t *testing.T) {
			var roundTrips atomic.Int32
			client := &http.Client{Transport: moderationRoundTripFunc(func(*http.Request) (*http.Response, error) {
				roundTrips.Add(1)
				return nil, errors.New("image request must not be sent")
			})}
			host := address
			if strings.Contains(address, ":") {
				host = "[" + address + "]"
			}
			_, err := prepareModerationImage(context.Background(), client, moderationImageInput{
				URL: "https://" + host + "/image.png",
			}, false)
			require.EqualError(t, err, "image URL host is not public")
			require.Zero(t, roundTrips.Load(), "private literal IP must be rejected before HTTP transport")
		})
	}
}

func TestModerationImageDialContextRejectsAllPrivateDNSBeforeDial(t *testing.T) {
	var dialCalls atomic.Int32
	dial := moderationImageDialContextWithResolver(
		&net.Dialer{},
		false,
		func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.10")}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			dialCalls.Add(1)
			return nil, errors.New("dial must not be attempted")
		},
	)

	_, err := dial(context.Background(), "tcp", "image.example:80")
	require.Error(t, err)
	var policyErr *moderationImagePolicyError
	require.ErrorAs(t, err, &policyErr)
	require.Zero(t, dialCalls.Load())
}

func TestModerationImageDialContextDialsOnlyAllowedMixedDNSAddress(t *testing.T) {
	var dialed []string
	dial := moderationImageDialContextWithResolver(
		&net.Dialer{},
		false,
		func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("1.1.1.1")}, nil
		},
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			client, peer := net.Pipe()
			_ = peer.Close()
			return client, nil
		},
	)

	conn, err := dial(context.Background(), "tcp", "image.example:80")
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Equal(t, []string{"1.1.1.1:80"}, dialed)
}

func TestIsPublicModerationImageIPRejectsNonPublicSpecialRanges(t *testing.T) {
	addresses := []string{
		"100.100.100.200",   // RFC 6598 CGNAT
		"192.0.0.1",         // IETF protocol assignments
		"192.0.2.1",         // TEST-NET-1
		"198.18.0.1",        // benchmarking
		"198.51.100.1",      // TEST-NET-2
		"203.0.113.1",       // TEST-NET-3
		"2001:db8::1",       // IPv6 documentation
		"2001::1",           // IPv6 benchmark/documentation range
		"64:ff9b::c000:201", // NAT64 well-known prefix carrying TEST-NET-1
	}
	for _, address := range addresses {
		t.Run(address, func(t *testing.T) {
			require.False(t, isPublicModerationImageIP(net.ParseIP(address)))
		})
	}
	for _, address := range []string{"1.1.1.1", "2001:4860:4860::8888"} {
		t.Run("public/"+address, func(t *testing.T) {
			require.True(t, isPublicModerationImageIP(net.ParseIP(address)))
		})
	}
}

type moderationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f moderationRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// moderationTestHandler mirrors NewHandler's route wiring while allowing the
// same-package tests to inject a counted/rejecting classifier transport or an
// image response-body read handshake. It does not add a production seam.
func moderationTestHandler(t *testing.T, config Config, client, imageClient *http.Client) (http.Handler, func()) {
	t.Helper()
	s := &server{
		config:       config,
		apiKey:       config.APIKey,
		adapterToken: config.AdapterToken,
		client:       client,
		imageClient:  imageClient,
	}
	cleanup := func() {}
	if config.Transport == "curl" {
		curlPath, err := exec.LookPath("curl")
		if err != nil {
			t.Skip("curl transport requested but curl is unavailable")
		}
		curlConfig, err := writeCurlConfig(config.APIKey)
		require.NoError(t, err)
		s.curlPath = curlPath
		s.curlConfigFile = curlConfig
		cleanup = func() { _ = os.Remove(curlConfig) }
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.Handle("GET /v1/models", s.authenticate(http.HandlerFunc(s.models)))
	mux.Handle("POST /v1/chat/completions", s.authenticate(http.HandlerFunc(s.classify)))
	mux.Handle("POST /v1/moderations", s.authenticate(http.HandlerFunc(s.moderate)))
	return mux, cleanup
}

type moderationBodyReadStarted struct {
	io.ReadCloser
	started chan<- struct{}
	once    sync.Once
}

func (b *moderationBodyReadStarted) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

type moderationBodyReadHandshakeTransport struct {
	base    http.RoundTripper
	started chan<- struct{}
}

func (t moderationBodyReadHandshakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &moderationBodyReadStarted{ReadCloser: resp.Body, started: t.started}
	return resp, nil
}

type countingModerationBody struct {
	reader    *bytes.Reader
	readBytes atomic.Int64
	closed    atomic.Bool
}

func (b *countingModerationBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.readBytes.Add(int64(n))
	return n, err
}

func (b *countingModerationBody) Close() error {
	b.closed.Store(true)
	return nil
}

func TestRequestModerationBoundsUnknownLengthImageAndClosesBody(t *testing.T) {
	body := &countingModerationBody{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, maxModerationImageBytes+2))}
	imageClient := &http.Client{Transport: moderationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          body,
			ContentLength: -1,
			Header:        make(http.Header),
			Request:       r,
		}, nil
	})}
	var upstreamRequests atomic.Int32
	upstreamClient := &http.Client{Transport: moderationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		upstreamRequests.Add(1)
		return nil, errors.New("DeepSeek must not be called")
	})}
	server := &server{
		config:      Config{Model: "deepseek-v4-flash-vision-exp"},
		client:      upstreamClient,
		imageClient: imageClient,
	}

	_, err := server.requestModeration(context.Background(), moderationInput{
		Text:   "caption",
		Images: []moderationImageInput{{URL: "https://image.example.test/image.png"}},
	})
	require.Error(t, err)
	var unsupportedErr *unsupportedModerationInputError
	require.ErrorAs(t, err, &unsupportedErr)
	require.Equal(t, int64(maxModerationImageBytes+1), body.readBytes.Load())
	require.True(t, body.closed.Load())
	require.Zero(t, upstreamRequests.Load())
}

func TestModerationsEndpointVisionRejectsChunkedOversizedImageWithoutUpstream(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	var imageFinished atomic.Bool
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		chunk := bytes.Repeat([]byte{'x'}, 64<<10)
		remaining := maxModerationImageBytes + 2
		for remaining > 0 {
			size := len(chunk)
			if remaining < size {
				size = remaining
			}
			if _, err := w.Write(chunk[:size]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			remaining -= size
		}
		imageFinished.Store(true)
	}))
	defer imageServer.Close()

	config := testConfig(upstream.URL)
	config.Model = "deepseek-v4-flash-vision-exp"
	handler, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	defer cleanup()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(
		`{"input":[{"type":"image_url","image_url":{"url":"`+imageServer.URL+`/image.png"}}]}`,
	)))

	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	require.Eventually(t, imageFinished.Load, time.Second, time.Millisecond)
	require.Zero(t, upstreamRequests.Load())
}

func TestCanonicalizeModerationDataURLReportsConcreteBoundaryErrors(t *testing.T) {
	_, err := canonicalizeModerationDataURL("data:image/png;base64," + strings.Repeat("A", maxModerationImageDataURLBytes))
	require.EqualError(t, err, "image data URL is too large")

	encodedTooLarge := base64.StdEncoding.EncodeToString(make([]byte, maxModerationImageBytes+1))
	_, err = canonicalizeModerationDataURL("data:image/png;base64," + encodedTooLarge)
	require.EqualError(t, err, "image is too large")

	_, err = canonicalizeModerationDataURL("data:image/png;base64,%%%")
	require.EqualError(t, err, "image data URL base64 is invalid")
}

func TestPrepareModerationImageClassifiesResolverFailureAsTransient(t *testing.T) {
	source := "https://image.example.invalid/image.png?token=image-secret"
	client := &http.Client{Transport: moderationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, source, r.URL.String())
		return nil, &net.DNSError{
			Err:         "temporary resolver failure",
			Name:        r.URL.Hostname(),
			IsTemporary: true,
		}
	})}

	_, err := prepareModerationImage(context.Background(), client, moderationImageInput{URL: source}, false)
	require.Error(t, err)
	var fetchErr *moderationImageFetchError
	require.ErrorAs(t, err, &fetchErr)
	require.Equal(t, http.StatusBadGateway, fetchErr.HTTPStatus())
	require.NotContains(t, safeModerationError(err), "image.example.invalid")
	require.NotContains(t, safeModerationError(err), "image-secret")
}

func TestPrepareModerationImageClassifiesAllPrivateDNSAsUnsupported(t *testing.T) {
	client := newModerationImageClient(time.Second, false)

	_, err := prepareModerationImage(context.Background(), client, moderationImageInput{
		URL: "https://localhost/image.png",
	}, false)
	require.Error(t, err)

	var unsupportedErr *unsupportedModerationInputError
	require.ErrorAs(t, err, &unsupportedErr)
}

func TestModerationImageFetchErrorsHaveDistinctHTTPSemantics(t *testing.T) {
	tests := []struct {
		name       string
		imageCode  int
		imageDelay time.Duration
		wantStatus int
		wantCode   string
	}{
		{name: "permanent not found", imageCode: http.StatusNotFound, wantStatus: http.StatusUnprocessableEntity, wantCode: "unsupported_input"},
		{name: "remote rate limit", imageCode: http.StatusTooManyRequests, wantStatus: http.StatusTooManyRequests, wantCode: "image_source_unavailable"},
		{name: "remote server failure", imageCode: http.StatusBadGateway, wantStatus: http.StatusBadGateway, wantCode: "image_source_unavailable"},
		{name: "remote timeout", imageDelay: 2 * time.Second, wantStatus: http.StatusBadGateway, wantCode: "image_source_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.imageDelay > 0 {
					time.Sleep(test.imageDelay)
				}
				if test.imageCode != 0 {
					w.WriteHeader(test.imageCode)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer imageServer.Close()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Image-fetch failures must stop before the DeepSeek request.
				t.Fatalf("DeepSeek stub must not be called for image fetch failure")
			}))
			defer upstream.Close()

			config := testConfig(upstream.URL)
			config.Model = "deepseek-v4-flash-vision-exp"
			config.Timeout = time.Second
			handler, cleanup, err := NewHandler(config)
			require.NoError(t, err)
			defer cleanup()

			imageURL := imageServer.URL + "/image.png?token=image-secret"
			body := `{"input":[{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]}`
			var logs bytes.Buffer
			previousLogWriter := log.Writer()
			log.SetOutput(&logs)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(body)))
			log.SetOutput(previousLogWriter)

			require.Equal(t, test.wantStatus, recorder.Code)
			var response struct {
				Error map[string]string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, test.wantCode, response.Error["code"])
			require.NotContains(t, logs.String(), imageURL)
			require.NotContains(t, logs.String(), "image-secret")
		})
	}
}

func TestModerationsEndpointRejectsInvalidClassificationAndMapsRetryableFailure(t *testing.T) {
	tests := []struct {
		name         string
		upstreamCode int
		content      string
		wantCode     int
	}{
		{name: "missing categories", upstreamCode: http.StatusOK, content: `{"safety":"Unsafe"}`, wantCode: http.StatusUnprocessableEntity},
		{name: "unknown category", upstreamCode: http.StatusOK, content: moderationUpstreamContent("Unsafe", "future"), wantCode: http.StatusUnprocessableEntity},
		{name: "safe contradiction", upstreamCode: http.StatusOK, content: moderationUpstreamContent("Safe", "violence"), wantCode: http.StatusUnprocessableEntity},
		{name: "risky without category", upstreamCode: http.StatusOK, content: moderationUpstreamContent("Unsafe"), wantCode: http.StatusUnprocessableEntity},
		{name: "rate limited", upstreamCode: http.StatusTooManyRequests, wantCode: http.StatusTooManyRequests},
		{name: "unavailable", upstreamCode: http.StatusServiceUnavailable, wantCode: http.StatusBadGateway},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.upstreamCode != http.StatusOK {
					w.WriteHeader(test.upstreamCode)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"choices": []map[string]any{{"message": map[string]any{"content": test.content}}},
				})
			}))
			defer upstream.Close()
			handler, cleanup, err := NewHandler(testConfig(upstream.URL))
			require.NoError(t, err)
			defer cleanup()

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations",
				strings.NewReader(`{"input":"test"}`)))
			require.Equal(t, test.wantCode, recorder.Code)
			if test.content != "" {
				require.NotContains(t, recorder.Body.String(), test.content)
			}
		})
	}
}
func TestModerationsEndpointValidatesAuthModelAndRequestShape(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	defer upstream.Close()
	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	tests := []struct {
		name     string
		body     string
		auth     bool
		wantCode int
	}{
		{name: "unauthorized", body: `{"input":"test"}`, wantCode: http.StatusUnauthorized},
		{name: "wrong model", body: `{"model":"omni-moderation-latest","input":"test"}`, auth: true, wantCode: http.StatusBadRequest},
		{name: "unknown field", body: `{"input":"test","extra":true}`, auth: true, wantCode: http.StatusBadRequest},
		{name: "empty input", body: `{"input":""}`, auth: true, wantCode: http.StatusUnprocessableEntity},
		{name: "unknown part", body: `{"input":[{"type":"audio","text":"x"}]}`, auth: true, wantCode: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(test.body))
			if test.auth {
				request.Header.Set("Authorization", "Bearer adapter-test-token")
			}
			handler.ServeHTTP(recorder, request)
			require.Equal(t, test.wantCode, recorder.Code)
		})
	}
}

func TestModerationsEndpointBoundsBatchAndInputRunes(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Safe"),
			}}},
		})
	}))
	defer upstream.Close()

	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	batch := make([]string, maxModerationBatchSize+1)
	for i := range batch {
		batch[i] = "item"
	}
	batchBody, err := json.Marshal(map[string]any{"input": batch})
	require.NoError(t, err)
	batchRecorder := httptest.NewRecorder()
	handler.ServeHTTP(batchRecorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(string(batchBody))))
	require.Equal(t, http.StatusUnprocessableEntity, batchRecorder.Code)
	require.Zero(t, requests.Load())

	longRecorder := httptest.NewRecorder()
	longInput := strings.Repeat("界", maxModerationInputRunes+1)
	longBody, err := json.Marshal(map[string]any{"input": longInput})
	require.NoError(t, err)
	handler.ServeHTTP(longRecorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(string(longBody))))
	require.Equal(t, http.StatusUnprocessableEntity, longRecorder.Code)
	require.Zero(t, requests.Load())

	longPartsRecorder := httptest.NewRecorder()
	partsBody, err := json.Marshal(map[string]any{"input": []map[string]string{
		{"type": "text", "text": strings.Repeat("界", maxModerationInputRunes/2)},
		{"type": "text", "text": strings.Repeat("界", maxModerationInputRunes/2)},
	}})
	require.NoError(t, err)
	handler.ServeHTTP(longPartsRecorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(string(partsBody))))
	require.Equal(t, http.StatusUnprocessableEntity, longPartsRecorder.Code)
	require.Zero(t, requests.Load())
}

func TestModerationsEndpointAppliesOneTotalTimeoutToBatch(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := requests.Add(1)
		if hit == 1 {
			select {
			case <-time.After(700 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		} else {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Safe"),
			}}},
		})
	}))
	defer upstream.Close()

	config := testConfig(upstream.URL)
	config.Timeout = time.Second
	handler, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	defer cleanup()

	start := time.Now()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":[\"first\",\"second\"]}")))
	elapsed := time.Since(start)
	upstream.CloseClientConnections()

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Equal(t, int32(2), requests.Load())
	require.Less(t, elapsed, 1500*time.Millisecond)
}

func TestModerationsEndpointRejectsDuplicateAndUnknownClassificationFields(t *testing.T) {
	tests := map[string]string{
		"duplicate top-level":  `{"safety":"Safe","safety":"Unsafe","categories":[]}`,
		"duplicate case field": `{"safety":"Safe","categories":[],"Categories":[]}`,
		"unknown top-level":    `{"safety":"Safe","categories":[],"extra":true}`,
		"duplicate category":   moderationUpstreamContent("Unsafe", "illicit", "illicit"),
		"unknown category":     moderationUpstreamContent("Unsafe", "future"),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			})
			require.NoError(t, err)
			_, err = parseModerationResponse(raw)
			require.Error(t, err)
		})
	}

	valid := moderationUpstreamContent("Safe")
	_, err := parseModerationResponse([]byte("{\"choices\":[{\"message\":{\"content\":\"" +
		strings.ReplaceAll(valid, "\"", "\\\"") +
		"\"}},{\"message\":{\"content\":\"unused\"}}]}"))
	require.Error(t, err)
}
func TestModerationsEndpointRejectsImageURLNullAndDuplicateRequestFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	defer upstream.Close()
	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	image := httptest.NewRecorder()
	handler.ServeHTTP(image, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":[{\"type\":\"text\",\"text\":\"caption\",\"image_url\":null}]}")))
	require.Equal(t, http.StatusUnprocessableEntity, image.Code)

	crossTyped := httptest.NewRecorder()
	handler.ServeHTTP(crossTyped, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader(`{"input":[{"type":"image_url","text":"hidden risk text","image_url":{"url":"`+tinyPNGDataURL()+`"}}]}`)))
	require.Equal(t, http.StatusUnprocessableEntity, crossTyped.Code)

	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":\"first\",\"input\":\"second\"}")))
	require.Equal(t, http.StatusBadRequest, duplicate.Code)
}

func TestCanonicalizeModerationImageRejectsExcessiveGIFFrames(t *testing.T) {
	frames := make([]*image.Paletted, maxModerationGIFFrames+1)
	for i := range frames {
		frames[i] = image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	}
	var encoded bytes.Buffer
	require.NoError(t, gif.EncodeAll(&encoded, &gif.GIF{Image: frames, Delay: make([]int, len(frames))}))
	_, err := canonicalizeModerationImageBytes(encoded.Bytes())
	require.ErrorContains(t, err, "resource limits")
}

func TestModerationImageCapacityFailsFast(t *testing.T) {
	s := &server{
		config:     Config{Model: "deepseek-v4-flash-vision-exp"},
		imageSlots: make(chan struct{}, 1),
	}
	s.imageSlots <- struct{}{}
	_, err := s.requestModeration(context.Background(), moderationInput{
		Images: []moderationImageInput{{URL: tinyPNGDataURL()}},
	})
	require.Error(t, err)
	var capacityErr *moderationCapacityError
	require.ErrorAs(t, err, &capacityErr)
}

func TestProductionModerationImageRejectsPlainHTTPBeforeFetch(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: moderationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("must not fetch plain HTTP")
	})}
	_, err := prepareModerationImage(context.Background(), client, moderationImageInput{
		URL: "http://1.1.1.1/image.png",
	}, false)
	require.ErrorContains(t, err, "must use HTTPS")
	require.Zero(t, requests.Load())
}

func TestRejectDuplicateJSONKeysRejectsNestedAndTrailingValues(t *testing.T) {
	require.Error(t, rejectDuplicateJSONKeys([]byte("{\"outer\":{\"value\":1,\"value\":2}}")))
	require.Error(t, rejectDuplicateJSONKeys([]byte("{\"value\":1} {\"value\":2}")))
	require.NoError(t, rejectDuplicateJSONKeys([]byte("{\"array\":[{\"value\":1},{\"value\":2}]}")))
	require.NoError(t, rejectDuplicateJSONKeys([]byte("null")))
}

func TestModerationsEndpointHonorsCallerCancellationAcrossBatch(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer upstream.Close()

	config := testConfig(upstream.URL)
	config.Timeout = time.Second
	handler, cleanup, err := NewHandler(config)
	require.NoError(t, err)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	request := testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":[\"first\",\"second\"]}")).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("moderation request did not stop after caller cancellation")
	}
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	upstream.CloseClientConnections()
}

func TestModerationsEndpointVisionHonorsCallerCancellationDuringImageDownload(t *testing.T) {
	transports := []string{"native"}
	if _, err := exec.LookPath("curl"); err == nil {
		transports = append(transports, "curl")
	}
	for _, transportName := range transports {
		t.Run(transportName, func(t *testing.T) {
			var deepSeekRequests atomic.Int32
			deepSeek := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				deepSeekRequests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer deepSeek.Close()

			imageReady := make(chan struct{})
			imageBodyReadStarted := make(chan struct{})
			imageCancelled := make(chan struct{})
			imageHandlerDone := make(chan struct{})
			imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(imageHandlerDone)
				w.Header().Set("Content-Type", "image/png")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				if _, err := w.Write([]byte("partial-image")); err != nil {
					return
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				close(imageReady)
				<-r.Context().Done()
				close(imageCancelled)
			}))
			defer imageServer.Close()

			config := testConfig(deepSeek.URL)
			config.Model = "deepseek-v4-flash-vision-exp"
			config.Transport = transportName
			config.Timeout = 5 * time.Second
			baseImageClient := newModerationImageClient(config.Timeout, true)
			imageClient := &http.Client{
				Transport: moderationBodyReadHandshakeTransport{
					base:    baseImageClient.Transport,
					started: imageBodyReadStarted,
				},
				Timeout:       baseImageClient.Timeout,
				CheckRedirect: baseImageClient.CheckRedirect,
			}
			handler, cleanup := moderationTestHandler(t, config, newHTTP1Client(config.Timeout), imageClient)
			defer cleanup()

			ctx, cancel := context.WithCancel(context.Background())
			request := testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(
				`{"input":[{"type":"image_url","image_url":{"url":"`+imageServer.URL+`/image.png"}}]}`,
			)).WithContext(ctx)
			recorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				handler.ServeHTTP(recorder, request)
				close(done)
			}()
			require.Eventually(t, func() bool {
				select {
				case <-imageReady:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond)
			require.Eventually(t, func() bool {
				select {
				case <-imageBodyReadStarted:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond)

			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("moderation request did not stop after image download cancellation")
			}
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Zero(t, deepSeekRequests.Load(), "classification must not start after image cancellation")
			require.Eventually(t, func() bool {
				select {
				case <-imageCancelled:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond)
			require.Eventually(t, func() bool {
				select {
				case <-imageHandlerDone:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond)
		})
	}
}

func TestModerationsEndpointVisionSharesTimeoutAcrossImageAndClassification(t *testing.T) {
	transports := []string{"native"}
	if _, err := exec.LookPath("curl"); err == nil {
		transports = append(transports, "curl")
	}
	for _, transportName := range transports {
		t.Run(transportName, func(t *testing.T) {
			imageBytes, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
			require.NoError(t, err)
			imageStarted := make(chan struct{})
			imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(imageStarted)
				w.Header().Set("Content-Type", "image/png")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case <-time.After(1100 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				_, _ = w.Write(imageBytes)
			}))
			defer imageServer.Close()

			classificationStarted := make(chan struct{})
			classificationFinished := make(chan struct{})
			classificationRelease := make(chan struct{})
			var deepSeekRequests atomic.Int32
			deepSeek := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				defer close(classificationFinished)
				deepSeekRequests.Add(1)
				close(classificationStarted)
				select {
				case <-r.Context().Done():
				case <-classificationRelease:
				}
			}))
			defer deepSeek.Close()

			config := testConfig(deepSeek.URL)
			config.Model = "deepseek-v4-flash-vision-exp"
			config.Transport = transportName
			config.Timeout = 2 * time.Second
			handler, cleanup, err := NewHandler(config)
			require.NoError(t, err)
			defer cleanup()

			start := time.Now()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations", strings.NewReader(
				`{"input":[{"type":"image_url","image_url":{"url":"`+imageServer.URL+`/image.png"}}]}`,
			)))
			elapsed := time.Since(start)
			close(classificationRelease)

			require.Equal(t, http.StatusBadGateway, recorder.Code, "a blocked classification must not return Safe")
			require.Less(t, elapsed, 2800*time.Millisecond, "image download and classification must share one timeout")
			require.Eventually(t, func() bool {
				select {
				case <-imageStarted:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond)
			require.Eventually(t, func() bool {
				select {
				case <-classificationStarted:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond)
			require.Eventually(t, func() bool {
				select {
				case <-classificationFinished:
					return true
				default:
					return false
				}
			}, time.Second, time.Millisecond)
			require.Equal(t, int32(1), deepSeekRequests.Load())
		})
	}
}

type adapterContractSettingRepo struct {
	values map[string]string
}

type adapterContractModerationRepo struct{}

func (*adapterContractModerationRepo) CreateLog(context.Context, *service.ContentModerationLog) error {
	return nil
}

func (*adapterContractModerationRepo) ExistingAPIKeyIDs(_ context.Context, ids []int64) ([]int64, error) {
	return ids, nil
}

func (*adapterContractModerationRepo) ListLogs(context.Context, service.ContentModerationLogFilter) ([]service.ContentModerationLog, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (*adapterContractModerationRepo) CountFlaggedByUserSince(context.Context, int64, time.Time, bool) (int, error) {
	return 0, nil
}

func (*adapterContractModerationRepo) CleanupExpiredLogs(context.Context, time.Time, time.Time) (*service.ContentModerationCleanupResult, error) {
	return &service.ContentModerationCleanupResult{}, nil
}

func (*adapterContractModerationRepo) UpdateLogEmailSent(context.Context, int64, bool) error {
	return nil
}

func (r *adapterContractSettingRepo) Get(_ context.Context, key string) (*service.Setting, error) {
	if value, ok := r.values[key]; ok {
		return &service.Setting{Key: key, Value: value}, nil
	}
	return nil, service.ErrSettingNotFound
}

func (r *adapterContractSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *adapterContractSettingRepo) Set(_ context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *adapterContractSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *adapterContractSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *adapterContractSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *adapterContractSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}
func TestContentModerationServiceToDeepSeekAdapterContract(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamRequests.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Unsafe", "illicit"),
			}}},
		})
	}))
	defer upstream.Close()

	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()
	adapter := httptest.NewServer(handler)
	defer adapter.Close()

	svc := service.NewContentModerationService(&adapterContractSettingRepo{}, nil, nil, nil, nil, nil, nil, nil)
	defer svc.Close()
	result, err := svc.TestAPIKeys(context.Background(), service.TestContentModerationAPIKeysInput{
		APIKeys:          []string{"adapter-test-token"},
		UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
		BaseURL:          adapter.URL,
		Model:            "deepseek-v4-flash",
		TimeoutMS:        2000,
		Prompt:           "classify this text",
	})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, "ok", result.Items[0].Status)
	require.Equal(t, http.StatusOK, result.Items[0].LastHTTPStatus)
	require.NotNil(t, result.AuditResult)
	require.Equal(t, 1.0, result.AuditResult.CategoryScores["illicit"])
	require.Equal(t, int32(1), upstreamRequests.Load())

	imageResult, err := svc.TestAPIKeys(context.Background(), service.TestContentModerationAPIKeysInput{
		APIKeys:          []string{"adapter-test-token"},
		UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
		BaseURL:          adapter.URL,
		Model:            "deepseek-v4-flash",
		TimeoutMS:        2000,
		Prompt:           "caption",
		Images:           []string{"data:image/png;base64,iVBORw0KGgo="},
	})
	require.NoError(t, err)
	require.Len(t, imageResult.Items, 1)
	require.Equal(t, "error", imageResult.Items[0].Status)
	require.Equal(t, http.StatusUnprocessableEntity, imageResult.Items[0].LastHTTPStatus)
	require.Nil(t, imageResult.AuditResult)
	require.Equal(t, int32(1), upstreamRequests.Load(), "image rejection must not reach DeepSeek")

	visionConfig := testConfig(upstream.URL)
	visionConfig.Model = "deepseek-v4-flash-vision-exp"
	visionHandler, visionCleanup, err := NewHandler(visionConfig)
	require.NoError(t, err)
	defer visionCleanup()
	visionAdapter := httptest.NewServer(visionHandler)
	defer visionAdapter.Close()

	visionResult, err := svc.TestAPIKeys(context.Background(), service.TestContentModerationAPIKeysInput{
		APIKeys:          []string{"adapter-test-token"},
		UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
		BaseURL:          visionAdapter.URL,
		Model:            visionConfig.Model,
		TimeoutMS:        2000,
		Prompt:           "caption",
		Images:           []string{tinyPNGDataURL()},
	})
	require.NoError(t, err)
	require.Len(t, visionResult.Items, 1)
	require.Equal(t, "ok", visionResult.Items[0].Status)
	require.Equal(t, http.StatusOK, visionResult.Items[0].LastHTTPStatus)
	require.NotNil(t, visionResult.AuditResult)
	require.Equal(t, 1, visionResult.ImageCount)
	require.Equal(t, int32(2), upstreamRequests.Load(), "vision image should reach DeepSeek stub")

	moderationConfig := service.ContentModerationConfig{
		Enabled:          true,
		Mode:             service.ContentModerationModeObserve,
		UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
		BaseURL:          adapter.URL,
		Model:            "deepseek-v4-flash",
		APIKeys:          []string{"adapter-test-token"},
		TimeoutMS:        2000,
		SampleRate:       100,
		AllGroups:        true,
		RetryCount:       0,
	}
	rawConfig, err := json.Marshal(moderationConfig)
	require.NoError(t, err)
	observeService := service.NewContentModerationService(&adapterContractSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled:      "true",
		service.SettingKeyContentModerationConfig: string(rawConfig),
	}}, &adapterContractModerationRepo{}, nil, nil, nil, nil, nil, nil)
	observeDecision, err := observeService.Check(context.Background(), service.ContentModerationCheckInput{
		Endpoint: "/v1/chat/completions",
		Model:    "gpt-5.5",
		Protocol: service.ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"caption"},{"type":"image_url","image_url":{"url":"` + tinyPNGDataURL() + `"}}]}]}`),
	})
	require.NoError(t, err)
	require.True(t, observeDecision.Allowed)
	require.False(t, observeDecision.Blocked)
	observeService.Close()
	require.Equal(t, int32(2), upstreamRequests.Load(), "observe-mode unsupported image must not reach DeepSeek")

	moderationConfig.Mode = service.ContentModerationModePreBlock
	rawConfig, err = json.Marshal(moderationConfig)
	require.NoError(t, err)
	preBlockService := service.NewContentModerationService(&adapterContractSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled:      "true",
		service.SettingKeyContentModerationConfig: string(rawConfig),
	}}, &adapterContractModerationRepo{}, nil, nil, nil, nil, nil, nil)
	preBlockDecision, err := preBlockService.Check(context.Background(), service.ContentModerationCheckInput{
		Endpoint: "/v1/chat/completions",
		Model:    "gpt-5.5",
		Protocol: service.ContentModerationProtocolOpenAIChat,
		Body:     []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"caption"},{"type":"image_url","image_url":{"url":"` + tinyPNGDataURL() + `"}}]}]}`),
	})
	preBlockService.Close()
	require.NoError(t, err)
	require.False(t, preBlockDecision.Allowed)
	require.True(t, preBlockDecision.Blocked)
	require.Equal(t, http.StatusServiceUnavailable, preBlockDecision.StatusCode)
	require.Equal(t, service.ContentModerationActionError, preBlockDecision.Action)
	require.Equal(t, int32(2), upstreamRequests.Load(), "pre-block unsupported image must not reach DeepSeek")
}

func TestContentModerationServicePreservesImageFetchFailureModes(t *testing.T) {
	tests := []struct {
		name              string
		imageStatus       int
		wantPreBlockBlock bool
	}{
		{name: "permanent image response remains unmoderatable", imageStatus: http.StatusNotFound, wantPreBlockBlock: true},
		{name: "transient remote image response is rejected in pre-block mode", imageStatus: http.StatusBadGateway, wantPreBlockBlock: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var deepSeekRequests atomic.Int32
			deepSeek := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				deepSeekRequests.Add(1)
				writeJSON(w, http.StatusOK, map[string]any{
					"choices": []map[string]any{{"message": map[string]any{
						"content": moderationUpstreamContent("Safe"),
					}}},
				})
			}))
			defer deepSeek.Close()

			imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.imageStatus)
			}))
			defer imageServer.Close()

			config := testConfig(deepSeek.URL)
			config.Model = "deepseek-v4-flash-vision-exp"
			handler, cleanup, err := NewHandler(config)
			require.NoError(t, err)
			defer cleanup()
			adapter := httptest.NewServer(handler)
			defer adapter.Close()

			moderationConfig := service.ContentModerationConfig{
				Enabled:          true,
				Mode:             service.ContentModerationModeObserve,
				UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
				BaseURL:          adapter.URL,
				Model:            config.Model,
				APIKeys:          []string{"adapter-test-token"},
				TimeoutMS:        2000,
				SampleRate:       100,
				AllGroups:        true,
				RetryCount:       0,
			}
			rawConfig, err := json.Marshal(moderationConfig)
			require.NoError(t, err)
			body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + imageServer.URL + `/image.png"}}]}]}`

			observeService := service.NewContentModerationService(&adapterContractSettingRepo{values: map[string]string{
				service.SettingKeyRiskControlEnabled:      "true",
				service.SettingKeyContentModerationConfig: string(rawConfig),
			}}, &adapterContractModerationRepo{}, nil, nil, nil, nil, nil, nil)
			observeDecision, err := observeService.Check(context.Background(), service.ContentModerationCheckInput{
				Endpoint: "/v1/chat/completions",
				Model:    "gpt-5.5",
				Protocol: service.ContentModerationProtocolOpenAIChat,
				Body:     []byte(body),
			})
			require.NoError(t, err)
			require.True(t, observeDecision.Allowed)
			require.False(t, observeDecision.Blocked)
			observeService.Close()

			moderationConfig.Mode = service.ContentModerationModePreBlock
			rawConfig, err = json.Marshal(moderationConfig)
			require.NoError(t, err)
			preBlockService := service.NewContentModerationService(&adapterContractSettingRepo{values: map[string]string{
				service.SettingKeyRiskControlEnabled:      "true",
				service.SettingKeyContentModerationConfig: string(rawConfig),
			}}, &adapterContractModerationRepo{}, nil, nil, nil, nil, nil, nil)
			preBlockDecision, err := preBlockService.Check(context.Background(), service.ContentModerationCheckInput{
				Endpoint: "/v1/chat/completions",
				Model:    "gpt-5.5",
				Protocol: service.ContentModerationProtocolOpenAIChat,
				Body:     []byte(body),
			})
			preBlockService.Close()
			require.NoError(t, err)
			if test.wantPreBlockBlock {
				require.False(t, preBlockDecision.Allowed)
				require.True(t, preBlockDecision.Blocked)
				require.Equal(t, http.StatusServiceUnavailable, preBlockDecision.StatusCode)
			} else {
				require.True(t, preBlockDecision.Allowed)
				require.False(t, preBlockDecision.Blocked)
			}
			require.Zero(t, deepSeekRequests.Load(), "image fetch failure must stop before DeepSeek")
		})
	}
}

func TestContentModerationServiceDoesNotFreezeKeysForImageSourceRateLimit(t *testing.T) {
	var imageRequests atomic.Int32
	var adapterRequests atomic.Int32
	var classificationRequests atomic.Int32
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		imageRequests.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer imageServer.Close()

	config := testConfig("https://api.deepseek.com/v1/chat/completions")
	config.Model = "deepseek-v4-flash-vision-exp"
	config.AdapterToken = "audit-key-one"
	classificationClient := &http.Client{Transport: moderationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		classificationRequests.Add(1)
		return nil, errors.New("classification must not be called")
	})}
	baseHandler, cleanup := moderationTestHandler(t, config, classificationClient, newModerationImageClient(config.Timeout, true))
	defer cleanup()
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adapterRequests.Add(1)
		baseHandler.ServeHTTP(w, r)
	}))
	defer adapter.Close()

	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock,
		UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
		BaseURL:          adapter.URL, Model: config.Model,
		APIKeys:   []string{"audit-key-one", "audit-key-two"},
		TimeoutMS: 2000, SampleRate: 100, AllGroups: true, RetryCount: 1,
	}
	rawConfig, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(&adapterContractSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled:      "true",
		service.SettingKeyContentModerationConfig: string(rawConfig),
	}}, &adapterContractModerationRepo{}, nil, nil, nil, nil, nil, nil)
	defer svc.Close()
	before, err := svc.GetStatus(context.Background())
	require.NoError(t, err)

	body, err := json.Marshal(map[string]any{
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type":      "image_url",
				"image_url": map[string]any{"url": imageServer.URL + "/image.png"},
			}},
		}},
	})
	require.NoError(t, err)
	decision, err := svc.Check(context.Background(), service.ContentModerationCheckInput{
		Endpoint: "/v1/chat/completions", Model: "gpt-5.5",
		Protocol: service.ContentModerationProtocolOpenAIChat,
		Body:     []byte(body),
	})
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.True(t, decision.Blocked)
	require.Equal(t, http.StatusServiceUnavailable, decision.StatusCode)

	after, err := svc.GetStatus(context.Background())
	require.NoError(t, err)
	require.Zero(t, imageRequests.Load(), "pre-block remote image must be rejected before fetching")
	require.Zero(t, adapterRequests.Load(), "pre-block remote image must be rejected before adapter dispatch")
	require.Zero(t, classificationRequests.Load())
	require.Equal(t, before.APIKeyStatuses, after.APIKeyStatuses)
	require.Equal(t, before.PreBlockAPIKeyLoads, after.PreBlockAPIKeyLoads)
	require.Equal(t, before.PreBlockAPIKeyTotalCalls, after.PreBlockAPIKeyTotalCalls)

	imageFailureAdapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"image_source_unavailable","message":"moderation image source unavailable"}}`)
	}))
	defer imageFailureAdapter.Close()
	testResult, err := svc.TestAPIKeys(context.Background(), service.TestContentModerationAPIKeysInput{
		APIKeys:          []string{"audit-key-one", "audit-key-two"},
		UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
		BaseURL:          imageFailureAdapter.URL,
		Model:            config.Model,
		TimeoutMS:        2000,
		Prompt:           "connection test",
	})
	require.NoError(t, err)
	require.Len(t, testResult.Items, 2)
	for _, item := range testResult.Items {
		require.Equal(t, "error", item.Status)
		require.Equal(t, http.StatusTooManyRequests, item.LastHTTPStatus)
		require.Zero(t, item.FailureCount)
		require.Nil(t, item.FrozenUntil)
	}
	finalStatus, err := svc.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, after.APIKeyStatuses, finalStatus.APIKeyStatuses)
}
func TestContentModerationServiceClassifiesPrivateDNSAsUnmoderatable(t *testing.T) {
	var adapterRequests atomic.Int32
	var deepSeekClassificationRequests atomic.Int32
	config := Config{
		ListenAddr:   "127.0.0.1:0",
		BaseURL:      "https://api.deepseek.com/v1/chat/completions",
		Model:        "deepseek-v4-flash-vision-exp",
		APIKey:       "deepseek-test-key",
		AdapterToken: "audit-key-one",
		Transport:    "native",
		Timeout:      2 * time.Second,
	}
	require.NoError(t, config.validate())
	classificationClient := &http.Client{Transport: moderationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		deepSeekClassificationRequests.Add(1)
		return nil, errors.New("DeepSeek classification must not be called")
	})}
	baseHandler, cleanup := moderationTestHandler(t, config, classificationClient, newModerationImageClient(config.Timeout, false))
	defer cleanup()
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adapterRequests.Add(1)
		baseHandler.ServeHTTP(w, r)
	}))
	defer adapter.Close()

	moderationConfig := service.ContentModerationConfig{
		Enabled:          true,
		Mode:             service.ContentModerationModeObserve,
		UpstreamProtocol: service.ContentModerationUpstreamProtocolOpenAIModerations,
		BaseURL:          adapter.URL,
		Model:            config.Model,
		APIKeys:          []string{"audit-key-one", "audit-key-two"},
		TimeoutMS:        1000,
		SampleRate:       100,
		AllGroups:        true,
		RetryCount:       1,
	}
	rawConfig, err := json.Marshal(moderationConfig)
	require.NoError(t, err)
	settings := &adapterContractSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled:      "true",
		service.SettingKeyContentModerationConfig: string(rawConfig),
	}}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://localhost/image.png"}}]}]}`)

	observeService := service.NewContentModerationService(settings, &adapterContractModerationRepo{}, nil, nil, nil, nil, nil, nil)
	observeBefore, err := observeService.GetStatus(context.Background())
	require.NoError(t, err)
	observeDecision, err := observeService.Check(context.Background(), service.ContentModerationCheckInput{
		Endpoint: "/v1/chat/completions",
		Model:    "gpt-5.5",
		Protocol: service.ContentModerationProtocolOpenAIChat,
		Body:     body,
	})
	require.NoError(t, err)
	require.True(t, observeDecision.Allowed)
	require.False(t, observeDecision.Blocked)
	require.Eventually(t, func() bool { return adapterRequests.Load() == 1 }, time.Second, time.Millisecond)
	observeService.Close()
	observeStatus, err := observeService.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, observeBefore.APIKeyStatuses, observeStatus.APIKeyStatuses, "unsupported input must not change audit key health")
	require.Equal(t, observeBefore.PreBlockAPIKeyLoads, observeStatus.PreBlockAPIKeyLoads, "observe must not change audit key load state")
	require.Len(t, observeStatus.APIKeyStatuses, 2)
	require.Equal(t, int32(1), adapterRequests.Load(), "observe must make one adapter attempt")
	require.Zero(t, deepSeekClassificationRequests.Load(), "unsupported image policy must stop before DeepSeek classification")
	require.Equal(t, int64(0), observeStatus.PreBlockAPIKeyTotalCalls)
	for _, status := range observeStatus.APIKeyStatuses {
		require.Equal(t, 0, status.FailureCount, "unsupported input must not mark an audit key failed")
		require.Zero(t, status.SuccessCount)
		require.Empty(t, status.LastError)
		require.False(t, status.LastTested)
	}

	moderationConfig.Mode = service.ContentModerationModePreBlock
	rawConfig, err = json.Marshal(moderationConfig)
	require.NoError(t, err)
	preBlockService := service.NewContentModerationService(&adapterContractSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled:      "true",
		service.SettingKeyContentModerationConfig: string(rawConfig),
	}}, &adapterContractModerationRepo{}, nil, nil, nil, nil, nil, nil)
	preBlockBefore, err := preBlockService.GetStatus(context.Background())
	require.NoError(t, err)
	preBlockDecision, err := preBlockService.Check(context.Background(), service.ContentModerationCheckInput{
		Endpoint: "/v1/chat/completions",
		Model:    "gpt-5.5",
		Protocol: service.ContentModerationProtocolOpenAIChat,
		Body:     body,
	})
	preBlockService.Close()
	require.NoError(t, err)
	require.False(t, preBlockDecision.Allowed)
	require.True(t, preBlockDecision.Blocked)
	require.Equal(t, http.StatusServiceUnavailable, preBlockDecision.StatusCode)
	require.Equal(t, service.ContentModerationActionError, preBlockDecision.Action)
	preBlockStatus, err := preBlockService.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, preBlockBefore.APIKeyStatuses, preBlockStatus.APIKeyStatuses, "unsupported input must not change audit key health")
	require.Equal(t, preBlockBefore.PreBlockAPIKeyLoads, preBlockStatus.PreBlockAPIKeyLoads, "unsupported input must not change audit key load state")
	require.Len(t, preBlockStatus.APIKeyStatuses, 2)
	require.Len(t, preBlockStatus.PreBlockAPIKeyLoads, 2)
	require.Equal(t, int32(1), adapterRequests.Load(), "pre-block remote image must be rejected before adapter dispatch")
	require.Zero(t, deepSeekClassificationRequests.Load(), "pre-block unsupported image must stop before DeepSeek classification")
	require.Equal(t, int64(0), preBlockStatus.PreBlockAPIKeyTotalCalls)
	for _, status := range preBlockStatus.APIKeyStatuses {
		require.Equal(t, 0, status.FailureCount, "unsupported input must not mark an audit key failed")
		require.Zero(t, status.SuccessCount)
		require.Empty(t, status.LastError)
		require.False(t, status.LastTested)
	}
}

func TestParseModerationClassificationMapsSafetyAndHierarchy(t *testing.T) {
	envelope := func(content string) []byte {
		raw, err := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
		require.NoError(t, err)
		return raw
	}

	safe, err := parseModerationResponse(envelope(moderationUpstreamContent("Safe")))
	require.NoError(t, err)
	require.Len(t, safe, len(moderationCategoryOrder))
	for _, score := range safe {
		require.Zero(t, score)
	}

	controversial, err := parseModerationResponse(envelope(moderationUpstreamContent("Controversial", "self-harm/intent")))
	require.NoError(t, err)
	require.Equal(t, 0.49, controversial["self-harm/intent"])
	require.Equal(t, 0.49, controversial["self-harm"])

	unsafe, err := parseModerationResponse(envelope(moderationUpstreamContent("Unsafe", "illicit/violent", "sexual/minors", "violence/graphic")))
	require.NoError(t, err)
	require.Equal(t, 1.0, unsafe["illicit/violent"])
	require.Equal(t, 1.0, unsafe["illicit"])
	require.Equal(t, 1.0, unsafe["sexual/minors"])
	require.Equal(t, 1.0, unsafe["sexual"])
	require.Equal(t, 1.0, unsafe["violence/graphic"])
	require.Equal(t, 1.0, unsafe["violence"])
}

func TestModerationsEndpointControversialIsObservableButNotFlagged(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Controversial", "harassment"),
			}}},
		})
	}))
	defer upstream.Close()
	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":\"borderline criticism\"}")))
	require.Equal(t, http.StatusOK, recorder.Code)
	var response moderationResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Results, 1)
	require.False(t, response.Results[0].Flagged)
	require.False(t, response.Results[0].Categories["harassment"])
	require.Equal(t, 0.49, response.Results[0].CategoryScores["harassment"])
}

func TestModerationsEndpointReturnsStructuredErrorCodes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent("Safe", "violence"),
			}}},
		})
	}))
	defer upstream.Close()
	handler, cleanup, err := NewHandler(testConfig(upstream.URL))
	require.NoError(t, err)
	defer cleanup()

	unusable := httptest.NewRecorder()
	handler.ServeHTTP(unusable, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":\"test\"}")))
	require.Equal(t, http.StatusUnprocessableEntity, unusable.Code)
	var unusableBody struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(unusable.Body.Bytes(), &unusableBody))
	require.Equal(t, "unusable_upstream_result", unusableBody.Error["code"])

	unsupported := httptest.NewRecorder()
	handler.ServeHTTP(unsupported, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"https://example.test/a.png\"}}]}")))
	require.Equal(t, http.StatusUnprocessableEntity, unsupported.Code)
	var unsupportedBody struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(unsupported.Body.Bytes(), &unsupportedBody))
	require.Equal(t, "unsupported_input", unsupportedBody.Error["code"])
}
