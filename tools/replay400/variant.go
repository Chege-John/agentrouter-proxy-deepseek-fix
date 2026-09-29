// Replay variants for a captured chat-completions body.
//
// Transformations remarshal JSON with HTML escaping off. The as-is variant
// returns the original bytes unchanged so a replay matches the capture.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// matrixVariants is the comparison set. content-thinking-* rows are extra:
// they target the content[].thinking wording while keeping reasoning text.
func matrixVariants() []string {
	return []string{
		"as-is",
		"strip-reasoning",
		"thinking-disabled",
		"thinking-disabled-strip",
		"chat-template-kwargs",
		"reasoning-effort:low",
		"reasoning-effort:medium",
		"reasoning-effort:high",
		"truncate:1",
		"truncate:1:strip",
		"truncate:2",
		"truncate:2:strip",
		"truncate:4",
		"truncate:4:strip",
		"reasoning-last-only",
		"stream-false",
		"content-thinking-blocks",
		"content-thinking-instead",
	}
}

// invalidVariants returns the rows that were previously invalidated by the circuit breaker.
func invalidVariants() []string {
	return []string{
		"reasoning-effort:low",
		"reasoning-effort:medium",
		"reasoning-effort:high",
		"truncate:1",
		"truncate:1:strip",
		"truncate:2",
		"truncate:2:strip",
		"truncate:4",
		"truncate:4:strip",
		"reasoning-last-only",
		"stream-false",
		"content-thinking-blocks",
		"content-thinking-instead",
	}
}

// applyVariant returns the body to send for name.
func applyVariant(raw []byte, name string) ([]byte, error) {
	if name == "as-is" {
		return append([]byte(nil), raw...), nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("body is not a JSON object")
	}

	switch {
	case name == "strip-reasoning":
		stripAllAssistantReasoning(body)
	case name == "thinking-disabled":
		body["thinking"] = map[string]any{"type": "disabled"}
	case name == "thinking-disabled-strip":
		body["thinking"] = map[string]any{"type": "disabled"}
		stripAllAssistantReasoning(body)
	case name == "chat-template-kwargs":
		setChatTemplateThinkingFalse(body)
	case strings.HasPrefix(name, "reasoning-effort:"):
		effort := strings.TrimPrefix(name, "reasoning-effort:")
		if effort == "" {
			return nil, fmt.Errorf("reasoning-effort needs a value")
		}
		body["reasoning_effort"] = effort
	case strings.HasPrefix(name, "truncate:"):
		n, strip, err := parseTruncate(name)
		if err != nil {
			return nil, err
		}
		if err := truncateRounds(body, n); err != nil {
			return nil, err
		}
		if strip {
			stripAllAssistantReasoning(body)
		}
	case name == "reasoning-last-only":
		keepLastToolReasoning(body)
	case name == "stream-false":
		body["stream"] = false
	case name == "content-thinking-blocks":
		addContentThinking(body, false)
	case name == "content-thinking-instead":
		addContentThinking(body, true)
	default:
		return nil, fmt.Errorf("unknown variant %q", name)
	}
	return marshalBody(body)
}

func parseTruncate(name string) (n int, strip bool, err error) {
	rest := strings.TrimPrefix(name, "truncate:")
	parts := strings.Split(rest, ":")
	if len(parts) == 0 || parts[0] == "" {
		return 0, false, fmt.Errorf("truncate needs N")
	}
	n, err = strconv.Atoi(parts[0])
	if err != nil || n < 1 {
		return 0, false, fmt.Errorf("truncate N must be a positive integer")
	}
	if len(parts) == 2 && parts[1] == "strip" {
		return n, true, nil
	}
	if len(parts) == 1 {
		return n, false, nil
	}
	return 0, false, fmt.Errorf("unknown truncate variant %q", name)
}

func marshalBody(body map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func messagesOf(body map[string]any) ([]any, bool) {
	msgs, ok := body["messages"].([]any)
	return msgs, ok
}

func eachAssistant(body map[string]any, fn func(map[string]any)) {
	msgs, ok := messagesOf(body)
	if !ok {
		return
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role == "assistant" {
			fn(mm)
		}
	}
}

func stripAllAssistantReasoning(body map[string]any) {
	eachAssistant(body, stripAssistantReasoning)
}

func stripAssistantReasoning(msg map[string]any) {
	delete(msg, "reasoning_content")
	delete(msg, "reasoning")
	delete(msg, "thinking")
	arr, ok := msg["content"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(arr))
	for _, p := range arr {
		pm, ok := p.(map[string]any)
		if !ok {
			kept = append(kept, p)
			continue
		}
		typ, _ := pm["type"].(string)
		if typ == "thinking" || typ == "reasoning" {
			continue
		}
		delete(pm, "thinking")
		delete(pm, "reasoning")
		delete(pm, "reasoning_content")
		kept = append(kept, pm)
	}
	msg["content"] = kept
}

func setChatTemplateThinkingFalse(body map[string]any) {
	kwargs, _ := body["chat_template_kwargs"].(map[string]any)
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	kwargs["thinking"] = false
	body["chat_template_kwargs"] = kwargs
}

// truncateRounds keeps the leading prefix (everything before the first
// assistant message) and the last n assistant rounds. A round is an assistant
// message plus the non-assistant messages that follow it until the next
// assistant message.
func truncateRounds(body map[string]any, n int) error {
	msgs, ok := messagesOf(body)
	if !ok {
		return fmt.Errorf("body has no messages array")
	}
	start := 0
	for start < len(msgs) && roleOf(msgs[start]) != "assistant" {
		start++
	}
	prefix := msgs[:start]
	var rounds [][]any
	for i := start; i < len(msgs); {
		j := i + 1
		for j < len(msgs) && roleOf(msgs[j]) != "assistant" {
			j++
		}
		rounds = append(rounds, msgs[i:j])
		i = j
	}
	if n >= len(rounds) {
		return nil
	}
	kept := make([]any, 0, len(prefix)+8)
	kept = append(kept, prefix...)
	for _, round := range rounds[len(rounds)-n:] {
		kept = append(kept, round...)
	}
	body["messages"] = kept
	return nil
}

func roleOf(m any) string {
	mm, ok := m.(map[string]any)
	if !ok {
		return ""
	}
	role, _ := mm["role"].(string)
	return role
}

func hasToolCalls(msg map[string]any) bool {
	tc, ok := msg["tool_calls"].([]any)
	return ok && len(tc) > 0
}

// keepLastToolReasoning strips reasoning on every assistant message except
// the last one that carries tool_calls. If none do, the last assistant
// message is the one kept.
func keepLastToolReasoning(body map[string]any) {
	msgs, ok := messagesOf(body)
	if !ok {
		return
	}
	last := -1
	lastAssistant := -1
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok || roleOf(m) != "assistant" {
			continue
		}
		lastAssistant = i
		if hasToolCalls(mm) {
			last = i
		}
	}
	if last < 0 {
		last = lastAssistant
	}
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok || roleOf(m) != "assistant" || i == last {
			continue
		}
		stripAssistantReasoning(mm)
	}
}

// addContentThinking copies each assistant reasoning_content string into a
// content part {"type":"thinking","thinking":...}. replace drops the
// reasoning_content key afterwards.
func addContentThinking(body map[string]any, replace bool) {
	eachAssistant(body, func(msg map[string]any) {
		rc, ok := msg["reasoning_content"].(string)
		if !ok || strings.TrimSpace(rc) == "" {
			return
		}
		block := map[string]any{"type": "thinking", "thinking": rc}
		switch c := msg["content"].(type) {
		case string:
			parts := []any{block}
			if c != "" {
				parts = append(parts, map[string]any{"type": "text", "text": c})
			}
			msg["content"] = parts
		case []any:
			if !contentHasThinking(c) {
				msg["content"] = append([]any{block}, c...)
			}
		default:
			if c == nil {
				msg["content"] = []any{block}
			}
		}
		if replace {
			delete(msg, "reasoning_content")
			delete(msg, "reasoning")
			delete(msg, "thinking")
		}
	})
}

func contentHasThinking(parts []any) bool {
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := pm["type"].(string); typ == "thinking" {
			return true
		}
		if _, ok := pm["thinking"]; ok {
			return true
		}
	}
	return false
}

// summarizeBody describes shape only: top-level key names, roles, and
// reasoning lengths. It does not include message text.
func summarizeBody(raw []byte) string {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "unparseable JSON"
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	// sort for stable output
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var b strings.Builder
	b.WriteString("keys=")
	b.WriteString(strings.Join(keys, ","))
	msgs, ok := body["messages"].([]any)
	if !ok {
		return b.String()
	}
	b.WriteString(" messages=")
	b.WriteString(strconv.Itoa(len(msgs)))
	b.WriteString(" shape=")
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte(' ')
		}
		mm, ok := m.(map[string]any)
		if !ok {
			b.WriteString("?")
			continue
		}
		role, _ := mm["role"].(string)
		if role == "" {
			role = "?"
		}
		if role == "assistant" {
			if rc, ok := mm["reasoning_content"].(string); ok {
				role += fmt.Sprintf("+R(%d)", len(rc))
			} else if _, ok := mm["reasoning_content"]; ok {
				role += "+R(nonstr)"
			}
			if hasToolCalls(mm) {
				role += "+T"
			}
			if arr, ok := mm["content"].([]any); ok && contentHasThinking(arr) {
				role += "+thinking"
			}
		}
		b.WriteString(role)
	}
	return b.String()
}
