package modeltrace_test

import (
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractForkAnnotation(t *testing.T) {
	t.Parallel()
	sessionID, messageID, turnID := modeltrace.ExtractForkAnnotation([]byte(`{
		"fork_from_session_id":"s-old",
		"fork_from_message_id":"m-1",
		"fork_from_turn_id":"t-1"
	}`), nil)
	require.Equal(t, "s-old", sessionID)
	require.Equal(t, "m-1", messageID)
	require.Equal(t, "t-1", turnID)

	sessionID, messageID, turnID = modeltrace.ExtractForkAnnotation([]byte(`{
		"client_metadata":{
			"fork_from_session_id":"s-cm",
			"fork_from_message_id":"m-cm"
		}
	}`), nil)
	require.Equal(t, "s-cm", sessionID)
	require.Equal(t, "m-cm", messageID)
	require.Empty(t, turnID)

	sessionID, messageID, turnID = modeltrace.ExtractForkAnnotation([]byte(`{"session_id":"only"}`), nil)
	require.Empty(t, sessionID)
	require.Empty(t, messageID)
	require.Empty(t, turnID)
}

func TestExtractForkAnnotation_codexForkedFromThreadID(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"client_metadata":{
			"session_id":"child",
			"x-codex-turn-metadata":"{\"session_id\":\"child\",\"turn_id\":\"t1\",\"request_kind\":\"turn\",\"forked_from_thread_id\":\"parent-thread\"}"
		}
	}`)
	sessionID, messageID, turnID := modeltrace.ExtractForkAnnotation(body, nil)
	require.Equal(t, "parent-thread", sessionID)
	require.Equal(t, modeltrace.WholeThreadForkMessageID, messageID)
	require.Empty(t, turnID)

	headers := http.Header{}
	headers.Set("X-Codex-Turn-Metadata", `{"forked_from_thread_id":"parent-hdr","request_kind":"turn"}`)
	sessionID, messageID, turnID = modeltrace.ExtractForkAnnotation([]byte(`{"client_metadata":{"session_id":"child"}}`), headers)
	require.Equal(t, "parent-hdr", sessionID)
	require.Equal(t, modeltrace.WholeThreadForkMessageID, messageID)
	require.Empty(t, turnID)
}

func TestIsCodexCompactionRequest(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"client_metadata":{
			"x-codex-turn-metadata":"{\"request_kind\":\"compaction\",\"compaction\":{\"trigger\":\"manual\"}}"
		}
	}`)
	require.True(t, modeltrace.IsCodexCompactionRequest(body, nil))
	require.False(t, modeltrace.IsCodexCompactionRequest([]byte(`{
		"client_metadata":{
			"x-codex-turn-metadata":"{\"request_kind\":\"turn\"}"
		}
	}`), nil))

	headers := http.Header{}
	headers.Set("X-Codex-Turn-Metadata", `{"request_kind":"compaction"}`)
	require.True(t, modeltrace.IsCodexCompactionRequest(nil, headers))
}
