package modeltrace

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// WriteConversationEvents emits chat.* child spans under the current turn span.
func WriteConversationEvents(ctx context.Context, tracer trace.Tracer, events []ChatEvent) {
	if tracer == nil || len(events) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, event := range events {
		if event.Name == "" || event.MessageID == "" {
			continue
		}
		meta := map[string]any{
			"session_id":        event.SessionID,
			"turn_id":           event.TurnID,
			"message_id":        event.MessageID,
			"parent_message_id": event.ParentMessageID,
			"seq":               event.Seq,
		}
		if event.CallID != "" {
			meta["call_id"] = event.CallID
		}
		if event.ToolName != "" {
			meta["tool_name"] = event.ToolName
		}
		if event.ForkFromSessionID != "" {
			meta["fork_from_session_id"] = event.ForkFromSessionID
		}
		if event.ForkFromMessageID != "" {
			meta["fork_from_message_id"] = event.ForkFromMessageID
		}
		if event.ForkFromTurnID != "" {
			meta["fork_from_turn_id"] = event.ForkFromTurnID
		}
		metadata, _ := json.Marshal(meta)

		_, span := tracer.Start(ctx, event.Name, trace.WithSpanKind(trace.SpanKindInternal))
		attrs := []attribute.KeyValue{
			attribute.String("langfuse.observation.type", "span"),
			attribute.String("langfuse.observation.name", event.Name),
			attribute.String("langfuse.observation.metadata", string(metadata)),
		}
		if event.SessionID != "" {
			attrs = append(attrs, attribute.String("langfuse.session.id", scrubURLsInString(event.SessionID)))
		}
		switch event.Name {
		case "chat.user", "chat.system", "chat.tool_result", "chat.compact", "chat.fork":
			attrs = append(attrs, attribute.String("langfuse.observation.input", scrubURLsInString(event.Content)))
		default:
			attrs = append(attrs, attribute.String("langfuse.observation.output", scrubURLsInString(event.Content)))
		}
		span.SetAttributes(attrs...)
		span.End()
	}
}

// recordConversationTrack loads existing IDs, extracts deltas, and writes chat.*.
// Failures never propagate to the request path.
func recordConversationTrack(ctx context.Context, tracer trace.Tracer, cfgEndpoint, publicKey, secretKey, sessionID string, input, output []byte, headers http.Header) {
	if sessionID == "" || tracer == nil {
		return
	}
	turnID := ExtractLangfuseTurnID(input, headers)
	if turnID == "" {
		turnID = MessageIDFromProtocolOrHash("", append([]byte(`{"generated_turn":true,"session_id":`), []byte(sessionID)...))[:16]
	}

	existing := map[string]string{}
	readOK := false
	if reader := langfuseReaderForTracing(cfgEndpoint, publicKey, secretKey); reader != nil {
		ids, err := reader.ListChatMessageIDs(ctx, sessionID)
		if err != nil {
			slog.Warn("modeltrace conversation read-back failed; writing all parsed chat items",
				"error", err.Error(),
			)
		} else {
			existing = ids
			readOK = true
		}
	} else {
		slog.Warn("modeltrace conversation read-back unavailable; writing all parsed chat items")
	}

	var existingForDelta map[string]string
	if readOK {
		existingForDelta = existing
	}

	normalizedOutput := NormalizeConversationOutput(output)
	events, compact := ExtractConversationDelta(sessionID, turnID, input, normalizedOutput, existingForDelta)
	if !compact && IsCodexCompactionRequest(input, headers) {
		events = prependCompactEvent(sessionID, turnID, events)
	}
	events = prependForkEvent(sessionID, turnID, input, headers, events, existing, readOK)
	WriteConversationEvents(ctx, tracer, events)
}

func prependForkEvent(sessionID, turnID string, input []byte, headers http.Header, events []ChatEvent, existing map[string]string, readOK bool) []ChatEvent {
	forkSession, forkMessage, forkTurn := ExtractForkAnnotation(input, headers)
	if forkSession == "" || forkMessage == "" {
		return events
	}
	forkMessageID := "fork:" + sessionID
	if readOK {
		if _, seen := existing[forkMessageID]; seen {
			return events
		}
	}
	forkEvent := ChatEvent{
		Name:              "chat.fork",
		MessageID:         forkMessageID,
		TurnID:            turnID,
		SessionID:         sessionID,
		ForkFromSessionID: forkSession,
		ForkFromMessageID: forkMessage,
		ForkFromTurnID:    forkTurn,
		Content:           forkSession + "@" + forkMessage,
		Seq:               0,
	}
	out := make([]ChatEvent, 0, len(events)+1)
	out = append(out, forkEvent)
	for i, event := range events {
		event.Seq = i + 1
		out = append(out, event)
	}
	return out
}

func prependCompactEvent(sessionID, turnID string, events []ChatEvent) []ChatEvent {
	for _, event := range events {
		if event.Name == "chat.compact" {
			return events
		}
	}
	canonical, _ := json.Marshal(map[string]string{
		"type":       "compact",
		"session_id": sessionID,
		"turn_id":    turnID,
		"source":     "codex_request_kind",
	})
	compactEvent := ChatEvent{
		Name:      "chat.compact",
		MessageID: MessageIDFromProtocolOrHash("", canonical),
		TurnID:    turnID,
		SessionID: sessionID,
		Content:   "codex_request_kind=compaction",
		Seq:       0,
	}
	out := make([]ChatEvent, 0, len(events)+1)
	out = append(out, compactEvent)
	for i, event := range events {
		event.Seq = i + 1
		out = append(out, event)
	}
	return out
}

// IsCodexCompactionRequest reports whether Codex marked this request as a
// compaction turn via turn metadata request_kind.
func IsCodexCompactionRequest(body []byte, headers http.Header) bool {
	if kind := codexRequestKind(body, headers); kind == "compaction" {
		return true
	}
	return false
}

func codexRequestKind(body []byte, headers http.Header) string {
	if len(body) > 0 && gjson.ValidBytes(body) {
		if kind := requestKindFromTurnMetadataJSON(gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()); kind != "" {
			return kind
		}
	}
	if headers != nil {
		if kind := requestKindFromTurnMetadataJSON(headers.Get("X-Codex-Turn-Metadata")); kind != "" {
			return kind
		}
	}
	return ""
}

func requestKindFromTurnMetadataJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(gjson.Get(raw, "request_kind").String()))
}
