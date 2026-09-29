package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ShapeAndFixReasoning returns a compact role summary of a chat-completions
// request for the REQ SHAPE log line: one tag per message, with assistant
// reasoning length annotations (+R(len) / +R(nonstr) / -R) and tool-call
// markers (+T). It never returns message content and never modifies the
// request body. It returns "" for non-chat paths or an unparseable body.
//
// Historical note: earlier versions rewrote assistant reasoning fields
// ("reasoning_content" / "Calling tools."). That mutation was removed; the
// request is now forwarded untouched.
func ShapeAndFixReasoning(raw []byte, path string) (shape string) {
	if !strings.HasPrefix(path, "/v1/chat/completions") {
		return ""
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	msgs, ok := body["messages"].([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role == "assistant" {
			rcVal, has := mm["reasoning_content"]
			_, tc := mm["tool_calls"]
			tag := "assistant"
			if !has {
				tag += "-R"
				if tc {
					tag += "+T"
				}
			} else {
				rcStr, isStr := rcVal.(string)
				if isStr {
					tag += fmt.Sprintf("+R(%d)", len(rcStr))
				} else {
					tag += "+R(nonstr)"
				}
				if tc {
					tag += "+T"
				}
			}
			role = tag
		}
		parts = append(parts, role)
	}
	if len(parts) > 30 {
		parts = append([]string{"..."}, parts[len(parts)-30:]...)
	}
	return strings.Join(parts, " ")
}
