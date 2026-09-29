package proxy

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestShapeAndFixReasoning(t *testing.T) {
	t.Run("assistant missing reasoning_content shows -R", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "user assistant-R" {
			t.Errorf("expected shape 'user assistant-R', got %q", shape)
		}
	})

	t.Run("existing reasoning_content shows length", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"assistant","content":"hello","reasoning_content":"thought process"}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "assistant+R(15)" {
			t.Errorf("expected shape 'assistant+R(15)', got %q", shape)
		}
	})

	t.Run("missing reasoning_content on tool-calling message shows -R+T", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"call tool","tool_calls":[{"id":"1"}]}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "assistant-R+T" {
			t.Errorf("expected shape 'assistant-R+T', got %q", shape)
		}
	})

	t.Run("whitespace-only reasoning on tool-calling message shows +R(3)+T", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"call tool","reasoning_content":"   ","tool_calls":[{"id":"1"}]}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "assistant+R(3)+T" {
			t.Errorf("expected shape 'assistant+R(3)+T', got %q", shape)
		}
	})

	t.Run("empty string reasoning on tool-calling message shows +R(0)+T", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"call tool","reasoning_content":"","tool_calls":[{"id":"1"}]}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "assistant+R(0)+T" {
			t.Errorf("expected shape 'assistant+R(0)+T', got %q", shape)
		}
	})

	t.Run("whitespace-only reasoning without tool calls shows +R(3)", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"hello","reasoning_content":"   "}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "assistant+R(3)" {
			t.Errorf("expected shape 'assistant+R(3)', got %q", shape)
		}
	})

	t.Run("non-string reasoning_content shows +R(nonstr)", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"hello","reasoning_content":123}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "assistant+R(nonstr)" {
			t.Errorf("expected shape 'assistant+R(nonstr)', got %q", shape)
		}
	})

	t.Run("model does not affect the shape", func(t *testing.T) {
		raw := []byte(`{"model":"gpt-4o","messages":[{"role":"assistant","content":"call tool","reasoning_content":"   ","tool_calls":[{"id":"1"}]}]}`)
		shape := ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if shape != "assistant+R(3)+T" {
			t.Errorf("expected shape 'assistant+R(3)+T', got %q", shape)
		}
	})

	t.Run("non-chat path returns empty shape", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"assistant","content":"hello"}]}`)
		for _, path := range []string{"/v1/messages", "/v1/completions", "/v1/models"} {
			if shape := ShapeAndFixReasoning(raw, path); shape != "" {
				t.Errorf("path %s: expected empty shape, got %q", path, shape)
			}
		}
	})

	t.Run("unparseable body returns empty shape", func(t *testing.T) {
		if shape := ShapeAndFixReasoning([]byte("not json"), "/v1/chat/completions"); shape != "" {
			t.Errorf("expected empty shape, got %q", shape)
		}
	})

	t.Run("body without messages returns empty shape", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-chat"}`)
		if shape := ShapeAndFixReasoning(raw, "/v1/chat/completions"); shape != "" {
			t.Errorf("expected empty shape, got %q", shape)
		}
	})

	t.Run("request bytes are never modified", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-chat","messages":[{"role":"assistant","content":"hello","reasoning_content":"   ","tool_calls":[{"id":"1"}]}]}`)
		snapshot := append([]byte(nil), raw...)
		_ = ShapeAndFixReasoning(raw, "/v1/chat/completions")

		if !bytes.Equal(raw, snapshot) {
			t.Errorf("input bytes changed:\nbefore: %s\nafter:  %s", snapshot, raw)
		}
	})

	t.Run("capping to 30", func(t *testing.T) {
		var msgs []string
		for i := 0; i < 35; i++ {
			msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"%d"}`, i))
		}
		capRaw := []byte(fmt.Sprintf(`{"model":"gpt-4o","messages":[%s]}`, strings.Join(msgs, ",")))
		capShape := ShapeAndFixReasoning(capRaw, "/v1/chat/completions")
		parts := strings.Split(capShape, " ")
		if len(parts) != 31 { // "..." + 30 roles
			t.Fatalf("expected 31 parts (including '...'), got %d: %s", len(parts), capShape)
		}
		if parts[0] != "..." {
			t.Errorf("expected first part to be '...', got %q", parts[0])
		}
	})
}
