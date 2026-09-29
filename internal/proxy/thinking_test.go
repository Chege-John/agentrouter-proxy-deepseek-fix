package proxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestAddThinkingDisabled(t *testing.T) {
	t.Run("adds thinking disabled to valid body", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello","reasoning_content":"thought"}]}`)
		out := AddThinkingDisabled(raw)

		var parsed map[string]any
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}

		// Verify thinking key
		thinking, ok := parsed["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("expected thinking to be a map, got %v", parsed["thinking"])
		}
		if thinking["type"] != "disabled" {
			t.Errorf("expected thinking.type='disabled', got %v", thinking["type"])
		}

		// Verify other fields preserved
		if parsed["model"] != "deepseek-v4-flash" {
			t.Errorf("model changed: %v", parsed["model"])
		}
		if parsed["stream"] != true {
			t.Errorf("stream changed: %v", parsed["stream"])
		}

		// Verify reasoning_content left untouched in history
		msgs := parsed["messages"].([]any)
		asst := msgs[1].(map[string]any)
		if asst["reasoning_content"] != "thought" {
			t.Errorf("reasoning_content was modified: %v", asst["reasoning_content"])
		}
	})

	t.Run("overwrites existing thinking", func(t *testing.T) {
		raw := []byte(`{"model":"deepseek-v4-flash","thinking":{"type":"enabled"}}`)
		out := AddThinkingDisabled(raw)

		var parsed map[string]any
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}
		thinking := parsed["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Errorf("expected thinking.type='disabled', got %v", thinking["type"])
		}
	})

	t.Run("returns original bytes on parse failure", func(t *testing.T) {
		cases := []struct {
			name string
			raw  []byte
		}{
			{"empty", []byte("")},
			{"not json", []byte("invalid json")},
			{"json array", []byte(`[1, 2, 3]`)},
			{"null", []byte("null")},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				out := AddThinkingDisabled(tc.raw)
				if !bytes.Equal(out, tc.raw) {
					t.Errorf("expected untouched bytes for %s", tc.name)
				}
			})
		}
	})
}
