package deepseekadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func moderationUpstreamContent(overrides map[string]float64) string {
	scores := make(map[string]float64, len(moderationCategoryOrder))
	for _, category := range moderationCategoryOrder {
		scores[category] = 0.01
	}
	for category, score := range overrides {
		scores[category] = score
	}
	raw, _ := json.Marshal(map[string]any{"category_scores": scores})
	return string(raw)
}

func TestModerationsEndpointReturnsOpenAICompatibleScores(t *testing.T) {
	var seen map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer deepseek-test-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent(map[string]float64{"illicit": 0.97}),
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
	require.Equal(t, 0.97, response.Results[0].CategoryScores["illicit"])
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
	require.Contains(t, userMessage["content"], "<BEGIN_UNTRUSTED_TEXT>")
}

func TestModerationsEndpointSupportsStringArrayAndTextParts(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"content": moderationUpstreamContent(nil),
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

func TestModerationsEndpointRejectsMalformedScoresAndMapsRetryableFailure(t *testing.T) {
	tests := []struct {
		name         string
		upstreamCode int
		content      string
		wantCode     int
	}{
		{name: "missing score", upstreamCode: http.StatusOK, content: `{"category_scores":{"violence":0.9}}`, wantCode: http.StatusUnprocessableEntity},
		{name: "out of range", upstreamCode: http.StatusOK, content: moderationUpstreamContent(map[string]float64{"violence": 1.1}), wantCode: http.StatusUnprocessableEntity},
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
				"content": moderationUpstreamContent(nil),
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
				"content": moderationUpstreamContent(nil),
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

func TestModerationsEndpointRejectsDuplicateJSONKeys(t *testing.T) {
	valid := moderationUpstreamContent(nil)
	duplicateTopLevel := strings.TrimSuffix(valid, "}") + ",\"category_scores\":{\"harassment\":0}}"
	duplicateCategory := strings.Replace(valid, "\"harassment\":0.01", "\"harassment\":0.01,\"harassment\":0.02", 1)
	unknownTopLevel := strings.TrimSuffix(valid, "}") + ",\"extra\":true}"
	unknownCategory := strings.Replace(valid, "\"violence\":0.01", "\"violence\":0.01,\"future\":0.01", 1)

	for name, content := range map[string]string{
		"duplicate top-level": duplicateTopLevel,
		"duplicate category":  duplicateCategory,
		"unknown top-level":   unknownTopLevel,
		"unknown category":    unknownCategory,
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			})
			require.NoError(t, err)
			_, err = parseModerationResponse(raw)
			require.Error(t, err)
		})
	}

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

	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, testRequest(http.MethodPost, "/v1/moderations",
		strings.NewReader("{\"input\":\"first\",\"input\":\"second\"}")))
	require.Equal(t, http.StatusBadRequest, duplicate.Code)
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

type adapterContractSettingRepo struct {
	values map[string]string
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
				"content": moderationUpstreamContent(map[string]float64{"illicit": 0.97}),
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
	require.Equal(t, 0.97, result.AuditResult.CategoryScores["illicit"])
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
}
