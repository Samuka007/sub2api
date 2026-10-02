package modeltrace

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// WholeThreadForkMessageID is used when a client reports a thread-level fork
// without a specific parent message id (Codex forked_from_thread_id).
const WholeThreadForkMessageID = "thread"

// ExtractForkAnnotation reads explicit fork fields from a request body and
// Codex turn-metadata headers. Heuristic fork detection stays out of scope.
func ExtractForkAnnotation(body []byte, headers http.Header) (forkFromSessionID, forkFromMessageID, forkFromTurnID string) {
	if len(body) > 0 && gjson.ValidBytes(body) {
		paths := []struct {
			session string
			message string
			turn    string
		}{
			{"fork_from_session_id", "fork_from_message_id", "fork_from_turn_id"},
			{"metadata.fork_from_session_id", "metadata.fork_from_message_id", "metadata.fork_from_turn_id"},
			{"client_metadata.fork_from_session_id", "client_metadata.fork_from_message_id", "client_metadata.fork_from_turn_id"},
		}
		for _, path := range paths {
			sessionID := stringField(gjson.GetBytes(body, path.session))
			messageID := stringField(gjson.GetBytes(body, path.message))
			if sessionID == "" || messageID == "" {
				continue
			}
			return sessionID, messageID, stringField(gjson.GetBytes(body, path.turn))
		}
		if sessionID, messageID, turnID := forkFromTurnMetadataJSON(gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()); sessionID != "" {
			return sessionID, messageID, turnID
		}
	}
	if headers != nil {
		if sessionID, messageID, turnID := forkFromTurnMetadataJSON(headers.Get("X-Codex-Turn-Metadata")); sessionID != "" {
			return sessionID, messageID, turnID
		}
	}
	return "", "", ""
}

func forkFromTurnMetadataJSON(raw string) (forkFromSessionID, forkFromMessageID, forkFromTurnID string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return "", "", ""
	}
	meta := gjson.Parse(raw)
	sessionID := firstNonEmpty(
		stringField(meta.Get("fork_from_session_id")),
		stringField(meta.Get("forked_from_thread_id")),
		stringField(meta.Get("forkedFromId")),
	)
	if sessionID == "" {
		return "", "", ""
	}
	messageID := firstNonEmpty(
		stringField(meta.Get("fork_from_message_id")),
		stringField(meta.Get("forked_from_message_id")),
	)
	if messageID == "" {
		messageID = WholeThreadForkMessageID
	}
	turnID := firstNonEmpty(
		stringField(meta.Get("fork_from_turn_id")),
		stringField(meta.Get("forked_from_turn_id")),
	)
	return sessionID, messageID, turnID
}
