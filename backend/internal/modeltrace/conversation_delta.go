package modeltrace

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ChatEvent is one append-only conversation observation payload.
type ChatEvent struct {
	Name              string
	MessageID         string
	TurnID            string
	SessionID         string
	ParentMessageID   string
	CallID            string
	ToolName          string
	Content           string
	Seq               int
	ForkFromSessionID string
	ForkFromMessageID string
	ForkFromTurnID    string
}

// ExtractConversationDelta parses current Responses-style input/output into
// append-only chat events. Repeated message IDs are intentionally preserved;
// offline export owns transcript deduplication.
func ExtractConversationDelta(sessionID, turnID string, input, output []byte) []ChatEvent {
	inputItems := conversationItemsFromPayload(input, true)
	outputItems := conversationItemsFromPayload(output, false)

	var events []ChatEvent
	var parent string
	seq := 0
	appendItem := func(item conversationItem) {
		if item.messageID == "" {
			return
		}
		if item.eventName == "chat.system" && strings.TrimSpace(item.content) == "" {
			return
		}
		// Do not consult persisted history here. Every request appends the current
		// event, including message IDs repeated by clients carrying full context.
		event := ChatEvent{
			Name:            item.eventName,
			MessageID:       item.messageID,
			TurnID:          turnID,
			SessionID:       sessionID,
			ParentMessageID: parent,
			CallID:          item.callID,
			ToolName:        item.toolName,
			Content:         item.content,
			Seq:             seq,
		}
		events = append(events, event)
		parent = item.messageID
		seq++
	}

	for _, item := range inputItems {
		appendItem(item)
	}
	for _, item := range outputItems {
		appendItem(item)
	}
	return events
}

type conversationItem struct {
	eventName string
	messageID string
	role      string
	callID    string
	toolName  string
	content   string
	canonical []byte
}

func conversationItemsFromPayload(payload []byte, preferInputField bool) []conversationItem {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return nil
	}
	root := gjson.ParseBytes(payload)
	switch {
	case root.IsArray():
		return conversationItemsFromArray(root.Array())
	case root.IsObject():
		if preferInputField {
			if input := root.Get("input"); input.Exists() {
				return conversationItemsFromValue(input)
			}
		}
		if output := root.Get("output"); output.Exists() {
			return conversationItemsFromValue(output)
		}
		if preferInputField {
			return nil
		}
		return conversationItemsFromValue(root)
	default:
		return nil
	}
}

func conversationItemsFromValue(value gjson.Result) []conversationItem {
	if value.IsArray() {
		return conversationItemsFromArray(value.Array())
	}
	if value.Type == gjson.String {
		text := strings.TrimSpace(value.String())
		if text == "" {
			return nil
		}
		canonical, _ := json.Marshal(map[string]string{
			"type":    "message",
			"role":    "user",
			"content": text,
		})
		return []conversationItem{{
			eventName: "chat.user",
			messageID: MessageIDFromProtocolOrHash("", canonical),
			role:      "user",
			content:   text,
			canonical: canonical,
		}}
	}
	if value.IsObject() {
		if item, ok := conversationItemFromObject(value); ok {
			return []conversationItem{item}
		}
	}
	return nil
}

func conversationItemsFromArray(values []gjson.Result) []conversationItem {
	items := make([]conversationItem, 0, len(values))
	for _, value := range values {
		if !value.IsObject() {
			continue
		}
		item, ok := conversationItemFromObject(value)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items
}

func conversationItemFromObject(value gjson.Result) (conversationItem, bool) {
	itemType := strings.TrimSpace(value.Get("type").String())
	role := strings.TrimSpace(value.Get("role").String())
	id := strings.TrimSpace(value.Get("id").String())
	callID := strings.TrimSpace(value.Get("call_id").String())
	name := strings.TrimSpace(value.Get("name").String())
	content := extractConversationText(value)

	switch {
	case itemType == "function_call" || (itemType == "" && callID != "" && name != "" && !value.Get("output").Exists()):
		canonical, _ := json.Marshal(map[string]string{
			"type":      "function_call",
			"call_id":   callID,
			"name":      name,
			"arguments": strings.TrimSpace(value.Get("arguments").String()),
		})
		messageID := MessageIDFromProtocolOrHash(firstNonEmpty(id, callID), canonical)
		return conversationItem{
			eventName: "chat.tool_call",
			messageID: messageID,
			callID:    callID,
			toolName:  name,
			content:   strings.TrimSpace(value.Get("arguments").String()),
			canonical: canonical,
		}, true
	case itemType == "function_call_output" || itemType == "tool_result":
		out := strings.TrimSpace(value.Get("output").String())
		if out == "" {
			out = content
		}
		canonical, _ := json.Marshal(map[string]string{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  out,
		})
		messageID := MessageIDFromProtocolOrHash(firstNonEmpty(id, callID+"_output"), canonical)
		return conversationItem{
			eventName: "chat.tool_result",
			messageID: messageID,
			callID:    callID,
			content:   out,
			canonical: canonical,
		}, true
	case itemType == "message" || role != "":
		if role == "" {
			role = "assistant"
		}
		eventName := "chat.assistant"
		switch role {
		case "user":
			eventName = "chat.user"
		case "system", "developer":
			eventName = "chat.system"
		}
		canonical, _ := json.Marshal(map[string]string{
			"type":    "message",
			"role":    role,
			"content": content,
		})
		messageID := MessageIDFromProtocolOrHash(id, canonical)
		return conversationItem{
			eventName: eventName,
			messageID: messageID,
			role:      role,
			content:   content,
			canonical: canonical,
		}, true
	default:
		return conversationItem{}, false
	}
}

func extractConversationText(value gjson.Result) string {
	if text := strings.TrimSpace(value.Get("content").String()); text != "" && value.Get("content").Type == gjson.String {
		return text
	}
	parts := value.Get("content").Array()
	var b strings.Builder
	for _, part := range parts {
		text := strings.TrimSpace(part.Get("text").String())
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			_ = b.WriteByte('\n')
		}
		_, _ = b.WriteString(text)
	}
	if b.Len() > 0 {
		return b.String()
	}
	return strings.TrimSpace(value.Get("text").String())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
