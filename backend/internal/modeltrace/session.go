package modeltrace

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	protocolAnthropicMessages = "anthropic.messages"
	protocolOpenAIResponses   = "openai.responses"
)

var claudeCodeLegacyUserIDPattern = regexp.MustCompile(`^user_[a-fA-F0-9]{64}_account_[a-fA-F0-9-]*_session_([a-fA-F0-9-]{36})$`)

// Correlation keeps client-owned session and thread identifiers independent.
// Sources are fixed low-cardinality values; conflicts never expose the losing ID.
type Correlation struct {
	SessionID       string
	SessionSource   string
	ThreadID        string
	ThreadSource    string
	SessionConflict bool
}

// ExtractCorrelation resolves explicit client correlation signals according to
// the entry protocol. Model names and inferred routing/cache identifiers are
// deliberately not inputs to this decision.
func ExtractCorrelation(body []byte, headers http.Header, protocol string, grokRoute bool) Correlation {
	correlation := Correlation{}
	bodySessionID, bodySessionSource := extractBodySession(body, protocol)
	headerSessionID, headerSessionSource := extractHeaderSession(headers, protocol, grokRoute)
	if bodySessionID != "" {
		correlation.SessionID = bodySessionID
		correlation.SessionSource = bodySessionSource
	} else {
		correlation.SessionID = headerSessionID
		correlation.SessionSource = headerSessionSource
	}
	correlation.SessionConflict = bodySessionID != "" && headerSessionID != "" && bodySessionID != headerSessionID

	bodyThreadID, bodyThreadSource := bodyString(body, "client_metadata.thread_id", "body.client_metadata.thread_id")
	headerThreadID, headerThreadSource := extractHeaderThread(headers)
	if bodyThreadID != "" {
		correlation.ThreadID = bodyThreadID
		correlation.ThreadSource = bodyThreadSource
	} else {
		correlation.ThreadID = headerThreadID
		correlation.ThreadSource = headerThreadSource
	}
	return correlation
}

// ExtractLangfuseSessionID is retained for callers that only need the session.
// Protocol-aware model tracing paths use ExtractCorrelation directly.
func ExtractLangfuseSessionID(body []byte, headers http.Header, grokRoute bool) string {
	return ExtractCorrelation(body, headers, "", grokRoute).SessionID
}

func extractBodySession(body []byte, protocol string) (string, string) {
	if value, source := bodyString(body, "session_id", "body.session_id"); value != "" {
		return value, source
	}
	if value, source := bodyString(body, "conversation_id", "body.conversation_id"); value != "" {
		return value, source
	}

	switch protocol {
	case protocolAnthropicMessages:
		if value, source := metadataString(body, "session_id", "body.metadata.session_id"); value != "" {
			return value, source
		}
		return metadataUserIDSession(body)
	case protocolOpenAIResponses:
		return bodyString(body, "client_metadata.session_id", "body.client_metadata.session_id")
	default:
		if value, source := metadataString(body, "session_id", "body.metadata.session_id"); value != "" {
			return value, source
		}
		// Preserve compatibility for direct callers that do not have a route.
		if protocol == "" {
			if value, source := metadataUserIDSession(body); value != "" {
				return value, source
			}
			return bodyString(body, "client_metadata.session_id", "body.client_metadata.session_id")
		}
	}
	return "", ""
}

func extractHeaderSession(headers http.Header, protocol string, grokRoute bool) (string, string) {
	if headers == nil {
		return "", ""
	}
	standard := func() (string, string) {
		return firstHeader(headers, []string{"Session-Id", "session_id"}, "header.session_id")
	}
	claude := func() (string, string) {
		return firstHeader(headers, []string{"X-Claude-Code-Session-Id"}, "header.x_claude_code_session_id")
	}

	if protocol == protocolAnthropicMessages {
		if value, source := claude(); value != "" {
			return value, source
		}
		return standard()
	}
	if value, source := standard(); value != "" {
		return value, source
	}
	if value, source := claude(); value != "" {
		return value, source
	}
	if (protocol == protocolOpenAIResponses || protocol == "") && grokRoute {
		return firstHeader(headers, []string{"X-Grok-Conv-Id"}, "header.x_grok_conv_id")
	}
	return "", ""
}

func extractHeaderThread(headers http.Header) (string, string) {
	if value, source := firstHeader(headers, []string{"Thread-Id", "thread_id"}, "header.thread_id"); value != "" {
		return value, source
	}
	raw := strings.TrimSpace(headers.Get("X-Codex-Turn-Metadata"))
	if raw == "" || !gjson.Valid(raw) {
		return "", ""
	}
	value := gjson.Get(raw, "thread_id")
	if !value.Exists() || value.Type != gjson.String {
		return "", ""
	}
	return strings.TrimSpace(value.String()), "header.x_codex_turn_metadata.thread_id"
}

func metadataUserIDSession(body []byte) (string, string) {
	metadata := validJSONResult(body, "metadata")
	if !metadata.Exists() {
		return "", ""
	}
	if metadata.Type == gjson.String {
		raw := strings.TrimSpace(metadata.String())
		if !gjson.Valid(raw) {
			return "", ""
		}
		metadata = gjson.Parse(raw)
	}
	userID := metadata.Get("user_id")
	if !userID.Exists() {
		return "", ""
	}
	if userID.IsObject() {
		value := userID.Get("session_id")
		if value.Exists() && value.Type == gjson.String {
			return strings.TrimSpace(value.String()), "body.metadata.user_id.json"
		}
		return "", ""
	}
	if userID.Type != gjson.String {
		return "", ""
	}
	raw := strings.TrimSpace(userID.String())
	if gjson.Valid(raw) {
		parsed := gjson.Parse(raw)
		if parsed.IsObject() {
			value := parsed.Get("session_id")
			if value.Exists() && value.Type == gjson.String {
				return strings.TrimSpace(value.String()), "body.metadata.user_id.json"
			}
		}
	}
	if matches := claudeCodeLegacyUserIDPattern.FindStringSubmatch(raw); len(matches) == 2 {
		return strings.TrimSpace(matches[1]), "body.metadata.user_id.legacy"
	}
	return "", ""
}

func metadataString(body []byte, field, source string) (string, string) {
	metadata := validJSONResult(body, "metadata")
	if !metadata.Exists() {
		return "", ""
	}
	if metadata.Type == gjson.String {
		raw := strings.TrimSpace(metadata.String())
		if !gjson.Valid(raw) {
			return "", ""
		}
		metadata = gjson.Parse(raw)
	}
	value := metadata.Get(field)
	if !value.Exists() || value.Type != gjson.String {
		return "", ""
	}
	return strings.TrimSpace(value.String()), source
}

func bodyString(body []byte, path, source string) (string, string) {
	value := validJSONResult(body, path)
	if !value.Exists() || value.Type != gjson.String {
		return "", ""
	}
	return strings.TrimSpace(value.String()), source
}

func validJSONResult(body []byte, path string) gjson.Result {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return gjson.Result{}
	}
	return gjson.GetBytes(body, path)
}

func firstHeader(headers http.Header, keys []string, source string) (string, string) {
	for _, key := range keys {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			return value, source
		}
	}
	return "", ""
}
