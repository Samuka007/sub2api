package modeltrace_test

import (
	"github.com/Wei-Shaw/sub2api/internal/modeltrace"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractConversationDelta_newUserAndAssistant(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]}
	]`
	output := `{"output":[{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":"hi"}]}]}`
	events, compact := modeltrace.ExtractConversationDelta("sess-1", "turn-1", []byte(input), []byte(output), map[string]string(nil))
	require.False(t, compact)
	require.Len(t, events, 2)
	require.Equal(t, "chat.user", events[0].Name)
	require.Equal(t, "u1", events[0].MessageID)
	require.Equal(t, "hello", events[0].Content)
	require.Equal(t, 0, events[0].Seq)
	require.Equal(t, "chat.assistant", events[1].Name)
	require.Equal(t, "a1", events[1].MessageID)
	require.Equal(t, "hi", events[1].Content)
	require.Equal(t, "u1", events[1].ParentMessageID)
}

func TestExtractConversationDelta_skipsExisting(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]},
		{"type":"message","role":"assistant","id":"a1","content":[{"type":"output_text","text":"hi"}]},
		{"type":"message","role":"user","id":"u2","content":[{"type":"input_text","text":"again"}]}
	]`
	existing := map[string]string{"u1": "chat.user", "a1": "chat.assistant"}
	events, compact := modeltrace.ExtractConversationDelta("sess-1", "turn-2", []byte(input), nil, existing)
	require.False(t, compact)
	require.Len(t, events, 1)
	require.Equal(t, "u2", events[0].MessageID)
}

func TestExtractConversationDelta_tools(t *testing.T) {
	t.Parallel()
	input := `[]`
	output := `{
		"output":[
			{"type":"function_call","id":"fc1","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"},
			{"type":"function_call_output","id":"fo1","call_id":"call_1","output":"result"}
		]
	}`
	events, compact := modeltrace.ExtractConversationDelta("sess-1", "turn-1", []byte(input), []byte(output), map[string]string{})
	require.False(t, compact)
	require.Len(t, events, 2)
	require.Equal(t, "chat.tool_call", events[0].Name)
	require.Equal(t, "call_1", events[0].CallID)
	require.Equal(t, "lookup", events[0].ToolName)
	require.Equal(t, "chat.tool_result", events[1].Name)
	require.Equal(t, "result", events[1].Content)
}

func TestExtractConversationDelta_compact(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u2","content":[{"type":"input_text","text":"later"}]}
	]`
	existing := map[string]string{"u1": "chat.user", "a1": "chat.assistant"}
	events, compact := modeltrace.ExtractConversationDelta("sess-1", "turn-3", []byte(input), nil, existing)
	require.True(t, compact)
	require.Len(t, events, 2)
	require.Equal(t, "chat.compact", events[0].Name)
	require.Equal(t, "u2", events[1].MessageID)
}

func TestExtractConversationDelta_forkMarkerDoesNotTriggerCompact(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"user","id":"u2","content":[{"type":"input_text","text":"later"}]}
	]`
	existing := map[string]string{
		"fork:sess-child": "chat.fork",
		"u2":              "chat.user",
	}
	events, compact := modeltrace.ExtractConversationDelta("sess-child", "turn-2", []byte(input), nil, existing)
	require.False(t, compact)
	require.Empty(t, events)
}

func TestExtractConversationDelta_includesDeveloperAndSystem(t *testing.T) {
	t.Parallel()
	input := `[
		{"type":"message","role":"developer","id":"d1","content":[{"type":"input_text","text":"system rules"}]},
		{"type":"message","role":"system","id":"s1","content":[{"type":"input_text","text":"policy"}]},
		{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"hello"}]}
	]`
	events, _ := modeltrace.ExtractConversationDelta("sess-1", "turn-1", []byte(input), nil, nil)
	require.Len(t, events, 3)
	require.Equal(t, "chat.system", events[0].Name)
	require.Equal(t, "d1", events[0].MessageID)
	require.Equal(t, "system rules", events[0].Content)
	require.Equal(t, "chat.system", events[1].Name)
	require.Equal(t, "s1", events[1].MessageID)
	require.Equal(t, "policy", events[1].Content)
	require.Equal(t, "chat.user", events[2].Name)
	require.Equal(t, "u1", events[2].MessageID)
}
