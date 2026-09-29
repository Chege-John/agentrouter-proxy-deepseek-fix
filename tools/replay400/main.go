// Command replay400 replays a captured chat-completions body against the local
// proxy and prints status codes plus upstream error wording.
//
// The capture contains your prompts. Do not share those files. This tool never
// prints the request body or the API key.
//
// Start the proxy with DISABLE_BREAKER=1 for replay runs. A flapping upstream
// can otherwise trip the circuit breaker mid-matrix and skew the results, and
// the tool no longer sends a per-request breaker bypass header.
//
//	$env:AR_API_KEY = "<your AgentRouter key>"   # or REPLAY_API_KEY
//	$env:DISABLE_BREAKER = "1"                    # proxy-side; set before starting it
//	go run ./tools/replay400 -body debug\<stamp>-request.json -matrix -n 8
//
// -matrix runs every variant. -variant NAME runs one (see matrixVariants).
// If PROXY_AUTH_TOKEN is set in the environment it is sent as X-Proxy-Token
// and is not printed. The upstream key is sent as Authorization: Bearer.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	bodyPath := flag.String("body", "", "path to a captured request JSON")
	n := flag.Int("n", 8, "attempts per variant")
	delay := flag.Duration("delay", time.Second, "pause between attempts")
	url := flag.String("url", "http://127.0.0.1:8318/v1/chat/completions", "proxy chat-completions URL")
	variant := flag.String("variant", "", "run a single variant name")
	matrix := flag.Bool("matrix", false, "run the full variant matrix")
	invalidOnly := flag.Bool("invalid-only", false, "run only the previously invalidated rows")
	timeout := flag.Duration("timeout", 90*time.Second, "per-attempt header/body timeout")
	flag.Parse()

	if *bodyPath == "" || (!*matrix && !*invalidOnly && *variant == "") {
		fmt.Fprintln(os.Stderr, "usage: replay400 -body request.json [-matrix | -invalid-only | -variant NAME] [-n 8]")
		fmt.Fprintln(os.Stderr, "variants:")
		for _, name := range matrixVariants() {
			fmt.Fprintln(os.Stderr, "  "+name)
		}
		os.Exit(2)
	}
	raw, err := os.ReadFile(*bodyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	names := matrixVariants()
	if *invalidOnly {
		names = invalidVariants()
	} else if *variant != "" {
		names = []string{*variant}
	}
	fmt.Println("shape:", summarizeBody(raw))
	fmt.Println("captures contain your prompts; do not share the debug files")

	key := os.Getenv("REPLAY_API_KEY")
	if key == "" {
		key = os.Getenv("AR_API_KEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "set REPLAY_API_KEY or AR_API_KEY in the environment")
		os.Exit(1)
	}
	proxyToken := os.Getenv("PROXY_AUTH_TOKEN")

	client := &http.Client{}
	rows := make([]row, 0, len(names))
	for _, name := range names {
		payload, err := applyVariant(raw, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			os.Exit(1)
		}
		attemptTimeout := *timeout
		if name == "stream-false" && attemptTimeout < 180*time.Second {
			attemptTimeout = 180 * time.Second
		}
		rows = append(rows, runVariant(client, *url, key, proxyToken, name, payload, *n, *delay, attemptTimeout))
	}
	printTable(rows)
}

type row struct {
	name     string
	ok       int
	bad      int
	five     int
	other    int
	wordings map[string]int
}

func runVariant(client *http.Client, url, key, proxyToken, name string, payload []byte, n int, delay, timeout time.Duration) row {
	r := row{name: name, wordings: map[string]int{}}
	for i := 1; i <= n; i++ {
		if i > 1 && delay > 0 {
			time.Sleep(delay)
		}
		status, words, err := postOnce(client, url, key, proxyToken, payload, timeout)
		switch {
		case err != nil:
			r.other++
			r.wordings[clip(err.Error(), 160)]++
			fmt.Printf("%s %d/%d error %s\n", name, i, n, clip(err.Error(), 160))
		case status == http.StatusOK:
			r.ok++
			fmt.Printf("%s %d/%d 200\n", name, i, n)
		case status == http.StatusBadRequest:
			r.bad++
			r.wordings[words]++
			fmt.Printf("%s %d/%d 400 %s\n", name, i, n, words)
		case status >= 500 && status <= 599:
			r.five++
			r.wordings[fmt.Sprintf("%d %s", status, words)]++
			fmt.Printf("%s %d/%d %d %s\n", name, i, n, status, words)
		default:
			r.other++
			label := fmt.Sprintf("%d %s", status, words)
			r.wordings[label]++
			fmt.Printf("%s %d/%d %s\n", name, i, n, label)
		}
	}
	return r
}

func postOnce(client *http.Client, url, key, proxyToken string, payload []byte, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	if proxyToken != "" {
		req.Header.Set("X-Proxy-Token", proxyToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	slurp, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, wording(slurp), nil
}

func wording(body []byte) string {
	var wrap struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &wrap) == nil {
		if wrap.Error.Message != "" {
			return clip(wrap.Error.Message, 180)
		}
		if wrap.Message != "" {
			return clip(wrap.Message, 180)
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	return clip(s, 180)
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func printTable(rows []row) {
	fmt.Println()
	fmt.Printf("%-28s %4s %4s %4s %5s  %s\n", "variant", "ok", "400", "5xx", "other", "wordings")
	for _, r := range rows {
		fmt.Printf("%-28s %4d %4d %4d %5d  %s\n", r.name, r.ok, r.bad, r.five, r.other, formatWordings(r.wordings))
	}
}

func formatWordings(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	type pair struct {
		s string
		n int
	}
	ps := make([]pair, 0, len(m))
	for s, n := range m {
		ps = append(ps, pair{s, n})
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].n == ps[j].n {
			return ps[i].s < ps[j].s
		}
		return ps[i].n > ps[j].n
	})
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = fmt.Sprintf("%s x%d", p.s, p.n)
	}
	return strings.Join(parts, " | ")
}
