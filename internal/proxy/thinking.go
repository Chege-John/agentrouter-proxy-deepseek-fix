package proxy

import (
	"encoding/json"
)

// AddThinkingDisabled adds top-level "thinking":{"type":"disabled"} to a JSON body.
// If raw is empty or unparseable, it returns raw unchanged.
// It changes nothing else in the body.
func AddThinkingDisabled(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return raw
	}
	body["thinking"] = map[string]any{"type": "disabled"}
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}
