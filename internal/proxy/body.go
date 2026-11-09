package proxy

import (
	"encoding/json"
	"strings"
)

// RequestSummary mirrors the shape returned by summarizeRequest().
type RequestSummary struct {
	Method       string
	Path         string
	BodyBytes    int
	ParseOK      bool
	Model        string
	Stream       bool
	MaxTokens    *int
	MessageCount *int
}

// SummarizeRequest parses the raw JSON body once and extracts the routing /
// telemetry fields used by the handler (utils.mjs summarizeRequest). MaxTokens
// and MessageCount are nil when the field is absent or of the wrong type; a
// non-string model yields "". ParseOK is false for any unparseable body.
func SummarizeRequest(raw []byte, path, method string) RequestSummary {
	s := RequestSummary{Method: method, Path: path, BodyBytes: len(raw)}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return s
	}
	s.ParseOK = true
	if m, ok := body["model"].(string); ok {
		s.Model = m
	}
	if b, ok := body["stream"].(bool); ok {
		s.Stream = b // body.stream === true only; other types are false
	}
	if mt, ok := body["max_tokens"].(float64); ok {
		v := int(mt)
		s.MaxTokens = &v
	}
	if msgs, ok := body["messages"].([]any); ok {
		n := len(msgs)
		s.MessageCount = &n
	}
	return s
}

// ResponseHasEmptyOutput reports whether a 200 response carries no usable
// model output (utils.mjs responseHasEmptyOutput): an Anthropic content array
// with no text-carrying part, or an OpenAI choices[0].message.content == "".
// Non-200 statuses, empty bodies and unparseable JSON are never "empty".
func ResponseHasEmptyOutput(statusCode int, body []byte) bool {
	if statusCode != 200 || len(body) == 0 {
		return false
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	if parts, ok := parsed["content"].([]any); ok {
		// content.every(part => part?.type !== "text" || !part.text): empty
		// arrays are vacuously "empty"; a part only fails when it is a text
		// part carrying a truthy (JS) text value.
		for _, part := range parts {
			pm, ok := part.(map[string]any)
			if !ok {
				continue // primitives / null: part?.type is undefined ≠ "text"
			}
			if t, _ := pm["type"].(string); t != "text" {
				continue
			}
			if jsFalsy(pm["text"]) {
				continue
			}
			return false
		}
		return true
	}
	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) == 0 {
		return false
	}
	first, ok := choices[0].(map[string]any)
	if !ok {
		return false
	}
	msg, ok := first["message"].(map[string]any)
	if !ok {
		return false
	}
	content, ok := msg["content"].(string)
	return ok && content == ""
}

// jsFalsy mirrors JavaScript truthiness for JSON values (the `!part.text`
// check): nil, false, "", 0 are falsy; everything else is truthy. (NaN cannot
// appear in JSON.)
func jsFalsy(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case float64:
		return t == 0
	default:
		return false
	}
}

// InjectPrompt prepends prompt into the request body (utils.mjs injectPrompt):
// `/v1/messages` bodies get the system prompt merged (a string system is
// prefixed with `prompt\n\n`, an array system has a text part unshifted, an
// absent system is created), `/v1/chat/completions` bodies get a system
// message unshifted. On parse failure, an empty prompt or an empty body the
// original bytes are returned unchanged.
//
// Known deviation: the re-marshalled body is compact JSON with keys sorted
// alphabetically (encoding/json), whereas Node's JSON.stringify preserves the
// original key order. The JSON is semantically identical.
func InjectPrompt(raw []byte, path, prompt string) []byte {
	if prompt == "" || len(raw) == 0 {
		return raw
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return raw
	}
	if strings.HasPrefix(path, "/v1/messages") {
		switch system := body["system"].(type) {
		case string:
			body["system"] = prompt + "\n\n" + system
		case []any:
			body["system"] = append([]any{
				map[string]any{"type": "text", "text": prompt},
			}, system...)
		default:
			body["system"] = []any{
				map[string]any{"type": "text", "text": prompt},
			}
		}
	}
	if strings.HasPrefix(path, "/v1/chat/completions") {
		if msgs, ok := body["messages"].([]any); ok {
			body["messages"] = append([]any{
				map[string]any{"role": "system", "content": prompt},
			}, msgs...)
		}
	}
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}
// SanitizeResponsesBody strips stateful Azure OpenAI identifiers from a
// `/v1/responses` request body so multi-turn requests do not fail with a 400
// when AgentRouter load-balances across multiple Azure OpenAI resources:
//  1. Sets `"store": false` at the top level.
//  2. Deletes `"include"` and `"previous_response_id"` from the top level.
//  3. Deletes `"reasoning"` from the top level when thinkingMode is `"off"`, or
//     when `"reasoning"` contains `"summary"` or `"effort": "none"`.
//  4. In `"input"` (when a slice of objects): removes items with `"type" == "reasoning"`,
//     deletes `"id"` and `"encrypted_content"` from remaining items (preserving `"call_id"`),
//     deletes `"status"` on `"message"` items, and strips `"id"` from nested `"content"` blocks.
//
// On non-`/v1/responses` paths, empty body, or JSON parse failure, raw is returned unchanged.
func SanitizeResponsesBody(raw []byte, path string, thinkingMode ...string) []byte {
	base := strings.SplitN(path, "?", 2)[0]
	if (base != "/v1/responses" && !strings.HasPrefix(base, "/v1/responses/")) || len(raw) == 0 {
		return raw
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return raw
	}
	body["store"] = false
	delete(body, "include")
	delete(body, "previous_response_id")

	mode := ""
	if len(thinkingMode) > 0 {
		mode = strings.ToLower(strings.TrimSpace(thinkingMode[0]))
	}
	if mode == "off" {
		delete(body, "reasoning")
	} else if rMap, ok := body["reasoning"].(map[string]any); ok {
		_, hasSummary := rMap["summary"]
		effort, _ := rMap["effort"].(string)
		if hasSummary || effort == "none" {
			delete(body, "reasoning")
		}
	}

	if input, ok := body["input"].([]any); ok {
		filtered := make([]any, 0, len(input))
		for _, item := range input {
			obj, isObj := item.(map[string]any)
			if !isObj {
				filtered = append(filtered, item)
				continue
			}
			itemType, _ := obj["type"].(string)
			if itemType == "reasoning" {
				continue
			}
			delete(obj, "id")
			delete(obj, "encrypted_content")
			if itemType == "message" {
				delete(obj, "status")
			}
			if contentArr, isArr := obj["content"].([]any); isArr {
				for _, cItem := range contentArr {
					if cMap, isMap := cItem.(map[string]any); isMap {
						delete(cMap, "id")
					}
				}
			}
			filtered = append(filtered, obj)
		}
		body["input"] = filtered
	}

	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}

