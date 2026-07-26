package modeltrace

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// NormalizeConversationOutput converts a captured client/upstream response body
// into a JSON document that ExtractConversationDelta can parse as assistant/tool
// items. Streaming Responses SSE is reduced to {"output":[...]} from the last
// response.completed / response.done event, with a fallback to
// response.output_item.done items when the completed event is missing/truncated.
func NormalizeConversationOutput(output []byte) []byte {
	if len(output) == 0 {
		return output
	}
	trimmed := bytes.TrimSpace(output)
	if gjson.ValidBytes(trimmed) {
		root := gjson.ParseBytes(trimmed)
		if root.IsObject() {
			if eventType := root.Get("type").String(); eventType == "response.completed" || eventType == "response.done" {
				if normalized := wrapConversationOutput(root.Get("response.output")); len(normalized) > 0 {
					return normalized
				}
			}
		}
		if root.IsObject() || root.IsArray() {
			return trimmed
		}
	}
	if !looksLikeSSE(trimmed) {
		return output
	}
	if normalized := conversationOutputFromSSE(trimmed); len(normalized) > 0 {
		return normalized
	}
	return output
}

func looksLikeSSE(payload []byte) bool {
	sample := payload
	if len(sample) > 2048 {
		sample = sample[:2048]
	}
	s := string(sample)
	return strings.Contains(s, "\ndata:") || strings.HasPrefix(s, "data:") || strings.Contains(s, "event:")
}

func conversationOutputFromSSE(payload []byte) []byte {
	var completedOutput json.RawMessage
	var itemDone []json.RawMessage

	for _, data := range iterSSEDataPayloads(payload) {
		if !gjson.Valid(data) {
			continue
		}
		eventType := gjson.Get(data, "type").String()
		switch eventType {
		case "response.completed", "response.done":
			out := gjson.Get(data, "response.output")
			if out.Exists() && out.IsArray() {
				completedOutput = json.RawMessage(out.Raw)
			}
		case "response.output_item.done":
			item := gjson.Get(data, "item")
			if item.Exists() && item.IsObject() {
				itemDone = append(itemDone, json.RawMessage(item.Raw))
			}
		}
	}

	var output json.RawMessage
	switch {
	case len(completedOutput) > 0:
		output = completedOutput
	case len(itemDone) > 0:
		joined, err := json.Marshal(itemDone)
		if err != nil {
			return nil
		}
		output = joined
	default:
		return nil
	}
	wrapped, err := json.Marshal(map[string]json.RawMessage{"output": output})
	if err != nil {
		return nil
	}
	return wrapped
}

func wrapConversationOutput(output gjson.Result) []byte {
	if !output.Exists() || !output.IsArray() {
		return nil
	}
	wrapped, err := json.Marshal(map[string]json.RawMessage{"output": json.RawMessage(output.Raw)})
	if err != nil {
		return nil
	}
	return wrapped
}

func iterSSEDataPayloads(payload []byte) []string {
	blocks := bytes.Split(payload, []byte("\n\n"))
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if len(bytes.TrimSpace(block)) == 0 {
			continue
		}
		var dataLines []string
		for _, line := range bytes.Split(block, []byte("\n")) {
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			dataLines = append(dataLines, string(bytes.TrimSpace(line[len("data:"):])))
		}
		if len(dataLines) == 0 {
			continue
		}
		joined := strings.Join(dataLines, "")
		if joined == "" || joined == "[DONE]" {
			continue
		}
		out = append(out, joined)
	}
	return out
}
