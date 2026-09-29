package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// passbackDump holds the previous successful upstream request while DUMP_400
// is enabled. dir overrides the default ./debug directory (tests only).
type passbackDump struct {
	mu     sync.Mutex
	lastOK []byte
	dir    string
}

var dumpSeq atomic.Uint64

// rememberOKBody keeps a copy of the body that just received HTTP 200.
// Later 400 captures write that copy out so the two requests can be diffed.
func (h *Handler) rememberOKBody(body []byte) {
	if h == nil || h.Cfg == nil || !h.Cfg.Dump400 || len(body) == 0 {
		return
	}
	cp := append([]byte(nil), body...)
	h.dumps.mu.Lock()
	h.dumps.lastOK = cp
	h.dumps.mu.Unlock()
}

// dumpPassedBack400 writes the request that drew a "must be passed back" 400,
// the upstream response body, and the previous successful request if one was
// seen. Files go under ./debug. Nothing from the bodies is written to the log,
// and headers (including Authorization) are not written.
func (h *Handler) dumpPassedBack400(reqBody, respBody []byte, attempt int, path string) {
	if h == nil || h.Cfg == nil || !h.Cfg.Dump400 {
		return
	}
	dir := h.dumps.dir
	if dir == "" {
		dir = "debug"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.Log.Info(fmt.Sprintf("DUMP_400: mkdir failed: %v", err))
		return
	}

	h.dumps.mu.Lock()
	var prev []byte
	if h.dumps.lastOK != nil {
		prev = append([]byte(nil), h.dumps.lastOK...)
	}
	h.dumps.mu.Unlock()

	stamp := time.Now().UTC().Format("20060102T150405.000Z")
	seq := dumpSeq.Add(1)
	base := fmt.Sprintf("%s-a%d-%d", stamp, attempt, seq)
	reqPath := filepath.Join(dir, base+"-request.json")
	respPath := filepath.Join(dir, base+"-response.json")
	if err := os.WriteFile(reqPath, reqBody, 0o600); err != nil {
		h.Log.Info(fmt.Sprintf("DUMP_400: write request failed: %v", err))
		return
	}
	if err := os.WriteFile(respPath, respBody, 0o600); err != nil {
		h.Log.Info(fmt.Sprintf("DUMP_400: write response failed: %v", err))
		return
	}

	prevLabel := "no"
	prevFile := ""
	if prev != nil {
		prevPath := filepath.Join(dir, base+"-prev-success.json")
		if err := os.WriteFile(prevPath, prev, 0o600); err != nil {
			h.Log.Info(fmt.Sprintf("DUMP_400: write previous success failed: %v", err))
		} else {
			prevLabel = "yes"
			prevFile = filepath.Base(prevPath)
		}
	}

	meta := map[string]any{
		"time":          time.Now().UTC().Format(time.RFC3339Nano),
		"attempt":       attempt,
		"status":        400,
		"path":          path,
		"requestBytes":  len(reqBody),
		"responseBytes": len(respBody),
		"requestFile":   filepath.Base(reqPath),
		"responseFile":  filepath.Base(respPath),
		"prevSuccess":   prevFile,
		"topLevelKeys":  topLevelKeys(reqBody),
	}
	if mb, err := json.MarshalIndent(meta, "", "  "); err == nil {
		metaPath := filepath.Join(dir, base+"-meta.json")
		if err := os.WriteFile(metaPath, append(mb, '\n'), 0o600); err != nil {
			h.Log.Info(fmt.Sprintf("DUMP_400: write meta failed: %v", err))
		}
	}

	h.Log.Info(fmt.Sprintf("DUMP_400: attempt %d keys=%s wrote %s prev=%s",
		attempt, joinKeys(topLevelKeys(reqBody)), reqPath, prevLabel))
}

func topLevelKeys(raw []byte) []string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinKeys(keys []string) string {
	if len(keys) == 0 {
		return "-"
	}
	out := keys[0]
	for _, k := range keys[1:] {
		out += "," + k
	}
	return out
}
