package modeltrace_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"mime/multipart"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestCaptureModelContentRedactsSecretsAndBounds(t *testing.T) {
	raw := []byte(fmt.Sprintf(`{"authorization":"Bearer auth-secret","api_key":"sk-secret","password":"pw-secret","prompt":%q}`, strings.Repeat("世界", 40)))
	got := modeltrace.TestingCaptureModelContent(raw, len(raw), 96, modeltrace.TestingCapturePolicy{})

	require.NotContains(t, got, "auth-secret")
	require.NotContains(t, got, "sk-secret")
	require.NotContains(t, got, "pw-secret")
	require.Contains(t, got, modeltrace.TestingRedactedValue)
	require.Contains(t, got, fmt.Sprintf("[truncated:original_bytes=%d,captured_bytes=", len(raw)))
	require.True(t, utf8.ValidString(got))

	captured := strings.Split(got, "[truncated:")[0]
	require.LessOrEqual(t, len([]byte(captured)), 96)
}

func TestCaptureModelContentOmitsMediaByDefault(t *testing.T) {
	binary := []byte("TOP_SECRET_BINARY")
	encoded := base64.StdEncoding.EncodeToString(binary)
	raw := []byte(fmt.Sprintf(`{
		"messages":[{"role":"user","content":[
			{"type":"image_url","image_url":{"url":"https://cdn.example.test/image.png?signature=url-secret"}},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}
		]}]}`, encoded))

	got := modeltrace.TestingCaptureModelContent(raw, len(raw), 4096, modeltrace.TestingCapturePolicy{MediaMaxBytes: 4})

	require.NotContains(t, got, "TOP_SECRET_BINARY")
	require.NotContains(t, got, encoded)
	require.NotContains(t, got, "url-secret")
	require.Contains(t, got, "https://cdn.example.test/image.png")
	require.Contains(t, got, `"media_type":"image/png"`)
	require.Contains(t, got, `"approx_bytes":17`)
	require.Contains(t, got, `"fingerprint":"sha256:`)
	require.NotContains(t, got, "content_base64")
}

func TestCaptureModelContentBoundsEachMediaIndependently(t *testing.T) {
	first := base64.StdEncoding.EncodeToString([]byte("abcdefgh"))
	second := base64.StdEncoding.EncodeToString([]byte("12345678"))
	raw := []byte(fmt.Sprintf(`{"content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}},
		{"type":"input_audio","input_audio":{"format":"wav","data":%q}}
	]}`, first, second))

	got := modeltrace.TestingCaptureModelContent(raw, len(raw), 4096, modeltrace.TestingCapturePolicy{
		MediaMaxBytes: 3, CaptureMediaContent: true,
	})

	require.NotContains(t, got, first)
	require.NotContains(t, got, second)
	require.Equal(t, 2, strings.Count(got, `"captured_bytes":3`))
	require.Equal(t, 2, strings.Count(got, `"truncated":true`))
	require.Contains(t, got, `"content_base64":"YWJj"`)
	require.Contains(t, got, `"content_base64":"MTIz"`)
}

func TestCaptureModelContentBoundsMultipartMediaOptIn(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "edit the image"))
	for index, content := range []string{"secret-image-one", "secret-image-two"} {
		part, err := writer.CreateFormFile(fmt.Sprintf("image_%d", index), fmt.Sprintf("image-%d.png", index))
		require.NoError(t, err)
		_, err = part.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	got := modeltrace.TestingCaptureModelContentWithType(body.Bytes(), body.Len(), 4096, writer.FormDataContentType(), modeltrace.TestingCapturePolicy{
		MediaMaxBytes: 4, CaptureMediaContent: true,
	})

	require.NotContains(t, got, "secret-image-one")
	require.NotContains(t, got, "secret-image-two")
	require.Contains(t, got, `"media_count":2`)
	require.Equal(t, 2, strings.Count(got, `"captured_bytes":4`))
	require.Equal(t, 2, strings.Count(got, `"content_base64":"c2Vjcg=="`))
	require.Equal(t, 2, strings.Count(got, `"truncated":true`))
}

func TestCaptureModelContentMalformedMultipartContentTypeFailsClosed(t *testing.T) {
	const canary = "malformed-multipart-canary-must-not-export"
	raw := []byte("--boundary\r\nContent-Disposition: form-data; name=\"image\"; filename=\"canary.png\"\r\n\r\n" + canary)

	got := modeltrace.TestingCaptureModelContentWithType(raw, len(raw), 4096, `multipart/form-data; boundary="unterminated`, modeltrace.TestingCapturePolicy{})

	require.NotContains(t, got, canary)
	require.Contains(t, got, "[MULTIPART OMITTED]")
}

func TestCaptureModelContentSanitizesTruncatedUnstructuredPrefix(t *testing.T) {
	media := base64.StdEncoding.EncodeToString([]byte("binary-secret"))
	prefix := []byte("Authorization: Bearer top-secret\n{\"api_key\":\"sk-secret\",\"image_url\":\"data:image/png;base64," + media + "\"")

	got := modeltrace.TestingCaptureModelContent(prefix, len(prefix)+100, 4096, modeltrace.TestingCapturePolicy{})

	require.NotContains(t, got, "top-secret")
	require.NotContains(t, got, "sk-secret")
	require.NotContains(t, got, media)
	require.Contains(t, got, modeltrace.TestingRedactedValue)
	require.Contains(t, got, fmt.Sprintf("[truncated:original_bytes=%d,captured_bytes=", len(prefix)+100))
}

func TestCaptureModelContentRedactsSecretsInsideSSEData(t *testing.T) {
	raw := []byte("data: {\"api_key\":\"sse-api-secret\",\"authorization\":\"Basic auth-secret\"}\n\n" +
		"data: {\"cookie\":\"session=sse-cookie-secret\",\"content\":\"ok\"}\n\n")

	got := modeltrace.TestingCaptureModelContent(raw, len(raw), 4096, modeltrace.TestingCapturePolicy{})

	for _, secret := range []string{"sse-api-secret", "auth-secret", "sse-cookie-secret"} {
		require.NotContains(t, got, secret)
	}
	require.Equal(t, 3, strings.Count(got, modeltrace.TestingRedactedValue))
}

func TestCaptureModelContentPreservesCredentialLikePromptText(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":"Explain Authorization: Bearer business-example and api_key=business-example verbatim"}]}`)

	got := modeltrace.TestingCaptureModelContent(raw, len(raw), 4096, modeltrace.TestingCapturePolicy{})

	require.Equal(t, string(raw), got)
	require.NotContains(t, got, modeltrace.TestingRedactedValue)
}
