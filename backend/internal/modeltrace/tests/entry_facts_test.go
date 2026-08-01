package modeltrace_test

import (
	"bytes"
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveEntryFacts(t *testing.T) {
	t.Run("JSON protocol and model", func(t *testing.T) {
		facts := modeltrace.TestingResolveEntryFacts("/v1/responses", "application/json", []byte(`{"model":"gpt-client"}`))
		require.Equal(t, "openai.responses", facts.Protocol)
		require.Equal(t, "gpt-client", facts.ClientModel)
	})

	t.Run("Responses subpaths keep the Responses protocol", func(t *testing.T) {
		for _, path := range []string{
			"/v1/responses/compact",
			"/responses/compact",
			"/backend-api/codex/responses/compact",
			"/v1/responses/foo/responses/compact",
		} {
			facts := modeltrace.TestingResolveEntryFacts(path, "application/json", []byte(`{"model":"gpt-client"}`))
			require.Equal(t, "openai.responses", facts.Protocol, "path=%s", path)
		}

		facts := modeltrace.TestingResolveEntryFacts("/v1/notresponses/compact", "application/json", []byte(`{"model":"gpt-client"}`))
		require.Empty(t, facts.Protocol)
	})

	t.Run("Live JSON model comes from session", func(t *testing.T) {
		facts := modeltrace.TestingResolveEntryFacts("/v1/live", "application/json", []byte(`{"sdp":"offer","session":{"model":"gpt-realtime"}}`))
		require.Equal(t, "openai.live", facts.Protocol)
		require.Equal(t, "gpt-realtime", facts.ClientModel)
	})

	t.Run("Codex Live multipart model comes from session", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("sdp", "offer"))
		require.NoError(t, writer.WriteField("session", `{"model":"gpt-realtime-codex"}`))
		require.NoError(t, writer.Close())

		facts := modeltrace.TestingResolveEntryFacts("/backend-api/codex/realtime/calls", writer.FormDataContentType(), body.Bytes())
		require.Equal(t, "openai.live", facts.Protocol)
		require.Equal(t, "gpt-realtime-codex", facts.ClientModel)
	})

	t.Run("Gemini model comes from path", func(t *testing.T) {
		facts := modeltrace.TestingResolveEntryFacts("/v1beta/models/gemini-client:streamGenerateContent", "application/json", []byte(`{"model":"ignored"}`))
		require.Equal(t, "gemini.streamGenerateContent", facts.Protocol)
		require.Equal(t, "gemini-client", facts.ClientModel)
	})

	t.Run("multipart model field", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "gpt-image-client"))
		part, err := writer.CreateFormFile("image", "image.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("binary-image"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		facts := modeltrace.TestingResolveEntryFacts("/v1/images/edits", writer.FormDataContentType(), body.Bytes())
		require.Equal(t, "openai.images.edits", facts.Protocol)
		require.Equal(t, "gpt-image-client", facts.ClientModel)
	})

	t.Run("entry facts stay bounded and valid UTF-8", func(t *testing.T) {
		model := strings.Repeat("界", 300)
		facts := modeltrace.TestingResolveEntryFacts("/v1/chat/completions", "application/json", []byte(`{"model":"`+model+`"}`))
		require.Equal(t, "openai.chat_completions", facts.Protocol)
		require.LessOrEqual(t, len(facts.ClientModel), modeltrace.TestingMaxEntryFactBytes)
		require.True(t, strings.HasPrefix(model, facts.ClientModel))
		require.NotContains(t, facts.ClientModel, "�")
	})
}
