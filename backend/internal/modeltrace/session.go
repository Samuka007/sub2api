package modeltrace

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ExtractSession returns only an explicit client-provided Langfuse session ID.
// Cache keys, routing hashes and request content are intentionally not session
// signals and are never used as fallbacks.
func ExtractSession(input []byte, headers http.Header) string {
	if sessionID := extractBodySession(input); sessionID != "" {
		return sessionID
	}

	for _, name := range []string{"session_id", "conversation_id", "x-grok-conv-id"} {
		if sessionID := strings.TrimSpace(headers.Get(name)); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

type sessionEnvelope struct {
	SessionID      json.RawMessage `json:"session_id"`
	ConversationID json.RawMessage `json:"conversation_id"`
	Metadata       json.RawMessage `json:"metadata"`
}

type sessionMetadata struct {
	SessionID json.RawMessage `json:"session_id"`
	UserID    json.RawMessage `json:"user_id"`
}

func extractBodySession(input []byte) string {
	if len(input) == 0 {
		return ""
	}

	var envelope sessionEnvelope
	if err := json.Unmarshal(input, &envelope); err != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{envelope.SessionID, envelope.ConversationID} {
		if sessionID := explicitJSONString(raw); sessionID != "" {
			return sessionID
		}
	}

	var metadata sessionMetadata
	if err := json.Unmarshal(envelope.Metadata, &metadata); err != nil {
		return ""
	}
	if sessionID := explicitJSONString(metadata.SessionID); sessionID != "" {
		return sessionID
	}
	return structuredUserIDSession(metadata.UserID)
}

func explicitJSONString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// Claude Code may carry structured metadata.user_id either as an object or as
// a JSON-encoded string. Only its explicit session_id member is accepted; the
// legacy synthetic user_..._session_... format is deliberately not parsed.
func structuredUserIDSession(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return ""
		}
		raw = json.RawMessage(encoded)
	}

	var structured struct {
		SessionID json.RawMessage `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &structured); err != nil {
		return ""
	}
	return explicitJSONString(structured.SessionID)
}
