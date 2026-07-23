package modeltrace

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// ExtractLangfuseTurnID returns an explicit client-owned turn identifier when
// present. Callers generate a per-request ID when this returns empty.
func ExtractLangfuseTurnID(body []byte, headers http.Header) string {
	if len(body) > 0 && gjson.ValidBytes(body) {
		if turnID := stringField(gjson.GetBytes(body, "client_metadata.turn_id")); turnID != "" {
			return turnID
		}
		if turnID := turnIDFromJSONString(gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()); turnID != "" {
			return turnID
		}
	}
	if headers != nil {
		if turnID := turnIDFromJSONString(headers.Get("X-Codex-Turn-Metadata")); turnID != "" {
			return turnID
		}
	}
	return ""
}

// MessageIDFromProtocolOrHash prefers a non-empty protocol item/call id;
// otherwise returns sha256 hex of the canonical JSON payload.
func MessageIDFromProtocolOrHash(protocolID string, canonicalJSON []byte) string {
	if id := strings.TrimSpace(protocolID); id != "" {
		return id
	}
	sum := sha256.Sum256(canonicalJSON)
	return hex.EncodeToString(sum[:])
}

func turnIDFromJSONString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return stringField(gjson.Get(raw, "turn_id"))
}

func stringField(value gjson.Result) string {
	if !value.Exists() || value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}
