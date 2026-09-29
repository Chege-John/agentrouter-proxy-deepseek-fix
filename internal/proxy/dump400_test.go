package proxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trefeon/agentrouter-spoof-proxy/internal/config"
)

func TestDumpPassedBack400Disabled(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{
		Cfg: &config.Config{},
		Log: slog.New(slog.NewTextHandler(ioDiscard{}, nil)),
	}
	h.dumps.dir = dir
	h.rememberOKBody([]byte(`{"secret":"prompt"}`))
	h.dumpPassedBack400([]byte(`{"secret":"prompt"}`), []byte(`{"error":"x"}`), 1, "/v1/chat/completions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dump wrote %d files with DUMP_400 off", len(entries))
	}
	if h.dumps.lastOK != nil {
		t.Fatal("rememberOKBody retained a body with DUMP_400 off")
	}
}

func TestDumpPassedBack400WritesBodiesNotLog(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	h := &Handler{
		Cfg: &config.Config{Dump400: true},
		Log: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	h.dumps.dir = dir

	const sentinel = "SECRET_PROMPT_XYZ_do_not_log"
	okBody := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"` + sentinel + `-ok"}]}`)
	reqBody := []byte(`{"model":"deepseek-v4-flash","stream":true,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"` + sentinel + `"}]}`)
	respBody := []byte(`{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API.","type":"invalid_request_error"}}`)

	h.rememberOKBody(okBody)
	h.dumpPassedBack400(reqBody, respBody, 2, "/v1/chat/completions")

	logText := logs.String()
	if strings.Contains(logText, sentinel) {
		t.Fatalf("log contains request body:\n%s", logText)
	}
	if strings.Contains(logText, "Authorization") {
		t.Fatalf("log contains Authorization:\n%s", logText)
	}
	if !strings.Contains(logText, "DUMP_400: attempt 2") || !strings.Contains(logText, "prev=yes") {
		t.Fatalf("log missing dump line:\n%s", logText)
	}
	if !strings.Contains(logText, "keys=") || !strings.Contains(logText, "thinking") {
		t.Fatalf("log missing top-level key names:\n%s", logText)
	}

	var reqFile, respFile, prevFile, metaFile string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, "-request.json"):
			reqFile = filepath.Join(dir, name)
		case strings.HasSuffix(name, "-response.json"):
			respFile = filepath.Join(dir, name)
		case strings.HasSuffix(name, "-prev-success.json"):
			prevFile = filepath.Join(dir, name)
		case strings.HasSuffix(name, "-meta.json"):
			metaFile = filepath.Join(dir, name)
		}
	}
	if reqFile == "" || respFile == "" || prevFile == "" || metaFile == "" {
		t.Fatalf("missing capture files in %s", dir)
	}
	gotReq, err := os.ReadFile(reqFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotReq, reqBody) {
		t.Fatal("request file is not the exact upstream body")
	}
	gotResp, err := os.ReadFile(respFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotResp, respBody) {
		t.Fatal("response file is not the exact upstream body")
	}
	gotPrev, err := os.ReadFile(prevFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPrev, okBody) {
		t.Fatal("previous success file mismatch")
	}
	metaRaw, err := os.ReadFile(metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metaRaw), sentinel) || strings.Contains(string(metaRaw), "Authorization") {
		t.Fatalf("meta contains body or authorization: %s", metaRaw)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["attempt"] != float64(2) {
		t.Fatalf("attempt = %v", meta["attempt"])
	}
	if _, ok := meta["authorization"]; ok {
		t.Fatal("meta has authorization field")
	}
}

func TestDumpPassedBack400NoPreviousSuccess(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	h := &Handler{
		Cfg: &config.Config{Dump400: true},
		Log: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	h.dumps.dir = dir
	h.dumpPassedBack400([]byte(`{"model":"m"}`), []byte(`{"error":{"message":"must be passed back"}}`), 1, "/v1/chat/completions")
	if !strings.Contains(logs.String(), "prev=no") {
		t.Fatalf("log = %s", logs.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "prev-success") {
			t.Fatal("wrote a previous-success file when none existed")
		}
	}
}

// ioDiscard is an io.Writer used when a test does not care about logs.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
