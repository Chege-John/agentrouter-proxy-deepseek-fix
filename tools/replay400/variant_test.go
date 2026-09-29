package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const fixture = `{
  "model": "deepseek-v4-flash",
  "stream": true,
  "stream_options": {"include_usage": true},
  "messages": [
    {"role": "system", "content": "SECRET_SYS"},
    {"role": "user", "content": "SECRET_USER"},
    {"role": "assistant", "content": "SECRET_A1", "reasoning_content": "think-1", "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "a", "arguments": "{}"}}]},
    {"role": "tool", "tool_call_id": "c1", "content": "SECRET_T1"},
    {"role": "assistant", "content": [{"type": "text", "text": "SECRET_A2"}, {"type": "thinking", "thinking": "nested"}], "reasoning_content": "think-2", "reasoning": "r2", "thinking": "th2"},
    {"role": "user", "content": "SECRET_USER2"},
    {"role": "assistant", "content": "", "reasoning_content": "think-3", "tool_calls": [{"id": "c2"}, {"id": "c3"}]},
    {"role": "tool", "tool_call_id": "c2", "content": "SECRET_T2"},
    {"role": "tool", "tool_call_id": "c3", "content": ""}
  ]
}`

func parseMsgs(t *testing.T, raw []byte) []any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body["messages"].([]any)
}

func TestApplyVariantAsIsPreservesBytes(t *testing.T) {
	raw := []byte(fixture)
	out, err := applyVariant(raw, "as-is")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(raw) {
		t.Fatal("as-is remarshalled the body")
	}
}

func TestStripRemovesNestedThinking(t *testing.T) {
	out, err := applyVariant([]byte(fixture), "strip-reasoning")
	if err != nil {
		t.Fatal(err)
	}
	msgs := parseMsgs(t, out)
	for i, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		for _, key := range []string{"reasoning_content", "reasoning", "thinking"} {
			if _, ok := mm[key]; ok {
				t.Errorf("message %d still has %s", i, key)
			}
		}
		if arr, ok := mm["content"].([]any); ok {
			for _, p := range arr {
				pm := p.(map[string]any)
				if pm["type"] == "thinking" {
					t.Errorf("message %d still has a thinking part", i)
				}
				if _, ok := pm["thinking"]; ok {
					t.Errorf("message %d content part still has thinking", i)
				}
			}
		}
	}
	user := msgs[1].(map[string]any)
	if user["content"] != "SECRET_USER" {
		t.Fatal("user content changed")
	}
}

func TestThinkingDisabledKeepsReasoning(t *testing.T) {
	out, err := applyVariant([]byte(fixture), "thinking-disabled")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	th := body["thinking"].(map[string]any)
	if th["type"] != "disabled" {
		t.Fatalf("thinking = %v", th)
	}
	asst := body["messages"].([]any)[2].(map[string]any)
	if asst["reasoning_content"] != "think-1" {
		t.Fatal("reasoning was dropped")
	}
	if !strings.Contains(string(out), "<") && strings.Contains(fixture, "<") {
		t.Fatal("unexpected escape check setup")
	}
}

func TestHTMLNotEscaped(t *testing.T) {
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"a < b & c"}]}`)
	out, err := applyVariant(raw, "stream-false")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "a < b & c") {
		t.Fatalf("escaped content: %s", out)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body["stream"] != false {
		t.Fatalf("stream = %v", body["stream"])
	}
}

func TestChatTemplateAndEffort(t *testing.T) {
	raw := []byte(`{"model":"m","chat_template_kwargs":{"mode":"x"},"messages":[]}`)
	out, err := applyVariant(raw, "chat-template-kwargs")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	kw := body["chat_template_kwargs"].(map[string]any)
	if kw["thinking"] != false || kw["mode"] != "x" {
		t.Fatalf("kwargs = %v", kw)
	}
	out, err = applyVariant([]byte(fixture), "reasoning-effort:high")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("effort = %v", body["reasoning_effort"])
	}
}

func TestTruncateKeepsPrefixAndLastRounds(t *testing.T) {
	out, err := applyVariant([]byte(fixture), "truncate:1")
	if err != nil {
		t.Fatal(err)
	}
	msgs := parseMsgs(t, out)
	// prefix is system, user, and the later user is inside round 2 (between assistants).
	// rounds:
	//  0 assistant c1 + tool
	//  1 assistant text + user SECRET_USER2
	//  2 assistant c2 + two tools
	// last 1 round starts at the assistant with c2.
	if len(msgs) != 2+1+2 { // system, user, assistant, tool, tool
		roles := make([]string, len(msgs))
		for i, m := range msgs {
			roles[i] = m.(map[string]any)["role"].(string)
		}
		t.Fatalf("len=%d roles=%v", len(msgs), roles)
	}
	if msgs[0].(map[string]any)["content"] != "SECRET_SYS" {
		t.Fatal("lost system prefix")
	}
	if msgs[1].(map[string]any)["content"] != "SECRET_USER" {
		t.Fatal("lost first user")
	}
	last := msgs[2].(map[string]any)
	if last["reasoning_content"] != "think-3" {
		t.Fatal("last round lost reasoning")
	}

	stripped, err := applyVariant([]byte(fixture), "truncate:1:strip")
	if err != nil {
		t.Fatal(err)
	}
	smsgs := parseMsgs(t, stripped)
	if _, ok := smsgs[2].(map[string]any)["reasoning_content"]; ok {
		t.Fatal("truncate strip kept reasoning")
	}
}

func TestReasoningLastOnly(t *testing.T) {
	out, err := applyVariant([]byte(fixture), "reasoning-last-only")
	if err != nil {
		t.Fatal(err)
	}
	msgs := parseMsgs(t, out)
	first := msgs[2].(map[string]any)
	if _, ok := first["reasoning_content"]; ok {
		t.Fatal("earlier tool assistant kept reasoning")
	}
	mid := msgs[4].(map[string]any)
	if _, ok := mid["reasoning_content"]; ok {
		t.Fatal("non-tool assistant kept reasoning")
	}
	last := msgs[6].(map[string]any)
	if last["reasoning_content"] != "think-3" {
		t.Fatal("last tool assistant lost reasoning")
	}
}

func TestContentThinkingBlocks(t *testing.T) {
	out, err := applyVariant([]byte(fixture), "content-thinking-blocks")
	if err != nil {
		t.Fatal(err)
	}
	msgs := parseMsgs(t, out)
	first := msgs[2].(map[string]any)
	if first["reasoning_content"] != "think-1" {
		t.Fatal("blocks variant dropped reasoning_content")
	}
	parts := first["content"].([]any)
	b0 := parts[0].(map[string]any)
	if b0["type"] != "thinking" || b0["thinking"] != "think-1" {
		t.Fatalf("thinking block = %v", b0)
	}
	b1 := parts[1].(map[string]any)
	if b1["text"] != "SECRET_A1" {
		t.Fatal("text part lost")
	}

	out, err = applyVariant([]byte(fixture), "content-thinking-instead")
	if err != nil {
		t.Fatal(err)
	}
	msgs = parseMsgs(t, out)
	first = msgs[2].(map[string]any)
	if _, ok := first["reasoning_content"]; ok {
		t.Fatal("instead variant kept reasoning_content")
	}
	parts = first["content"].([]any)
	if parts[0].(map[string]any)["thinking"] != "think-1" {
		t.Fatal("instead variant missing thinking text")
	}
}

func TestSummarizeOmitsText(t *testing.T) {
	s := summarizeBody([]byte(fixture))
	if strings.Contains(s, "SECRET_") {
		t.Fatalf("summary leaked content: %s", s)
	}
	if !strings.Contains(s, "assistant+R(7)+T") || !strings.Contains(s, "keys=") {
		t.Fatalf("summary = %s", s)
	}
	if !strings.Contains(s, "thinking") {
		t.Fatalf("summary missing thinking key or part marker: %s", s)
	}
}
