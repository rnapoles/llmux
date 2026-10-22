package gateway_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rnapoles/llmux/internal/backend"
	"github.com/rnapoles/llmux/internal/config"
	"github.com/rnapoles/llmux/internal/dialect"
	"github.com/rnapoles/llmux/internal/gateway"
	"github.com/rnapoles/llmux/internal/inspector"
)

// fakeOpenAI mimics an OpenAI-compatible server and echoes what it received.
func fakeOpenAI(t *testing.T, seen *http.Header, body *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if seen != nil {
			*seen = r.Header.Clone()
		}
		if body != nil {
			*body = string(b)
		}
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"real-model","object":"model","created":1,"owned_by":"x"}]}`)
		case "/v1/chat/completions":
			var req struct{ Stream bool }
			_ = json.Unmarshal(b, &req)
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fl := w.(http.Flusher)
				for _, tok := range []string{"Hel", "lo"} {
					fmt.Fprintf(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", tok)
					fl.Flush()
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"real-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

// fakeOllama mimics a native Ollama server.
func fakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"llama3:latest","model":"llama3:latest","modified_at":"2024-01-01T00:00:00Z","size":42}]}`)
		case "/api/chat":
			var req struct{ Stream *bool }
			_ = json.Unmarshal(b, &req)
			if req.Stream != nil && !*req.Stream {
				fmt.Fprint(w, `{"model":"llama3","created_at":"2024-01-01T00:00:00Z","message":{"role":"assistant","content":"Hello"},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}`)
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprintln(w, `{"model":"llama3","message":{"role":"assistant","content":"Hel"},"done":false}`)
			fmt.Fprintln(w, `{"model":"llama3","message":{"role":"assistant","content":"lo"},"done":false}`)
			fmt.Fprintln(w, `{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":3,"eval_count":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newGateway(t *testing.T, hosts ...config.Host) (*httptest.Server, *inspector.Store) {
	t.Helper()
	cfg := config.Default()
	cfg.Hosts = hosts
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	store := inspector.NewStore(100, 1<<20)
	gw, err := gateway.New(cfg, gateway.Deps{Dialects: dialect.Default(), Backends: backend.Default(), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	return srv, store
}

func do(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range hdr {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestProxyHeadersAuthModelMap(t *testing.T) {
	var seen http.Header
	var gotBody string
	up := fakeOpenAI(t, &seen, &gotBody)
	defer up.Close()

	gw, store := newGateway(t, config.Host{
		Name:     "main",
		Dialects: []string{"openai"},
		APIKeys:  []string{"client-key"},
		Upstream: config.Upstream{Type: "openai", URL: up.URL, APIKey: "upstream-secret"},
		ModelMap: map[string]string{"gpt-4": "real-model"},
		Headers: config.Headers{
			Request:  config.HeaderRules{Set: map[string]string{"X-Tenant": "acme", "User-Agent": "llmux-test"}, Remove: []string{"X-Secret"}},
			Response: config.HeaderRules{Set: map[string]string{"X-Served-By": "llmux"}},
		},
	})

	// unauthorised
	resp, _ := do(t, "POST", gw.URL+"/v1/chat/completions", `{}`, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}

	resp, body := do(t, "POST", gw.URL+"/v1/chat/completions",
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer client-key", "Content-Type": "application/json", "X-Secret": "s", "X-Trace": "t1"})
	if resp.StatusCode != 200 || !strings.Contains(body, "Hello") {
		t.Fatalf("unexpected %d %s", resp.StatusCode, body)
	}
	if got := seen.Get("Authorization"); got != "Bearer upstream-secret" {
		t.Errorf("client key must be replaced by upstream key, got %q", got)
	}
	if seen.Get("X-Tenant") != "acme" || seen.Get("User-Agent") != "llmux-test" {
		t.Errorf("header override failed: %v", seen)
	}
	if seen.Get("X-Secret") != "" {
		t.Errorf("X-Secret should have been removed")
	}
	if seen.Get("X-Trace") != "t1" {
		t.Errorf("X-Trace should be forwarded")
	}
	if !strings.Contains(gotBody, `"real-model"`) {
		t.Errorf("model not mapped: %s", gotBody)
	}
	if resp.Header.Get("X-Served-By") != "llmux" {
		t.Errorf("response header override missing")
	}

	// inspector captured both legs and usage
	rec := store.List()[1].Snapshot()
	if rec.Mode != "proxy" || rec.Upstream.Status != 200 || rec.Prompt != 3 || rec.Completion != 2 {
		t.Errorf("bad record: mode=%s up=%d usage=%d/%d", rec.Mode, rec.Upstream.Status, rec.Prompt, rec.Completion)
	}
	if !strings.Contains(rec.Upstream.ReqHeader.Get("Authorization"), "••") {
		t.Errorf("upstream auth header should be redacted in the inspector")
	}
}

func TestOllamaClientToOpenAIUpstream(t *testing.T) {
	up := fakeOpenAI(t, nil, nil)
	defer up.Close()
	gw, store := newGateway(t, config.Host{
		Name: "x", Dialects: []string{"ollama"}, Upstream: config.Upstream{Type: "openai", URL: up.URL},
	})

	// non-stream
	_, body := do(t, "POST", gw.URL+"/api/chat", `{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}]}`, nil)
	var r struct {
		Message struct{ Content string }
		Done    bool
		Eval    int `json:"eval_count"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil || r.Message.Content != "Hello" || !r.Done || r.Eval != 2 {
		t.Fatalf("bad ollama response: %s", body)
	}

	// stream => NDJSON
	_, body = do(t, "POST", gw.URL+"/api/chat", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil)
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], `"done":true`) || !strings.Contains(lines[2], `"eval_count":2`) {
		t.Fatalf("bad ndjson: %q", body)
	}

	// tags + root
	_, body = do(t, "GET", gw.URL+"/api/tags", "", nil)
	if !strings.Contains(body, "real-model") {
		t.Fatalf("tags: %s", body)
	}
	_, body = do(t, "GET", gw.URL+"/", "", nil)
	if body != "Ollama is running" {
		t.Fatalf("root: %q", body)
	}
	if rec := store.List()[1].Snapshot(); rec.Mode != "translate" || !rec.Stream {
		t.Errorf("record: mode=%s stream=%v", rec.Mode, rec.Stream)
	}
}

func TestOpenAIClientToOllamaUpstreamStreaming(t *testing.T) {
	up := fakeOllama(t)
	defer up.Close()
	gw, _ := newGateway(t, config.Host{
		Name: "x", Dialects: []string{"openai"}, Upstream: config.Upstream{Type: "ollama", URL: up.URL},
	})

	_, body := do(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`, nil)
	if !strings.Contains(body, `"Hello"`) || !strings.Contains(body, `"completion_tokens":2`) {
		t.Fatalf("non-stream: %s", body)
	}

	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"llama3","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	var events []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if l := sc.Text(); strings.HasPrefix(l, "data: ") {
			events = append(events, strings.TrimPrefix(l, "data: "))
		}
	}
	if events[len(events)-1] != "[DONE]" {
		t.Fatalf("no [DONE]: %v", events)
	}
	joined := strings.Join(events, "\n")
	for _, want := range []string{`"role":"assistant"`, `"content":"Hel"`, `"content":"lo"`, `"finish_reason":"stop"`, `"total_tokens":5`} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}

	_, body = do(t, "GET", gw.URL+"/v1/models", "", nil)
	if !strings.Contains(body, `"llama3:latest"`) {
		t.Fatalf("models: %s", body)
	}
}

func TestVirtualHosts(t *testing.T) {
	a := fakeOpenAI(t, nil, nil)
	defer a.Close()
	b := fakeOllama(t)
	defer b.Close()
	gw, _ := newGateway(t,
		config.Host{Name: "a", Hosts: []string{"openai.local"}, Upstream: config.Upstream{Type: "openai", URL: a.URL}},
		config.Host{Name: "b", Hosts: []string{"*.ollama.local"}, Upstream: config.Upstream{Type: "ollama", URL: b.URL}},
	)
	_, body := do(t, "GET", gw.URL+"/v1/models", "", map[string]string{"Host": "openai.local:8080"})
	if !strings.Contains(body, "real-model") {
		t.Fatalf("host a: %s", body)
	}
	_, body = do(t, "GET", gw.URL+"/v1/models", "", map[string]string{"Host": "x.ollama.local"})
	if !strings.Contains(body, "llama3") {
		t.Fatalf("host b: %s", body)
	}
	resp, _ := do(t, "GET", gw.URL+"/v1/models", "", map[string]string{"Host": "unknown.example"})
	if resp.StatusCode != 404 {
		t.Fatalf("unknown host should 404, got %d", resp.StatusCode)
	}
}

func TestPassthroughStreaming(t *testing.T) {
	up := fakeOpenAI(t, nil, nil)
	defer up.Close()
	gw, store := newGateway(t, config.Host{Name: "x", Dialects: []string{"openai"}, Upstream: config.Upstream{Type: "openai", URL: up.URL}})
	_, body := do(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"m","stream":true,"messages":[]}`, nil)
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("body: %s", body)
	}
	rec := store.List()[0].Snapshot()
	if !rec.Stream || rec.Prompt != 3 || rec.Completion != 2 {
		t.Errorf("stream=%v usage=%d/%d", rec.Stream, rec.Prompt, rec.Completion)
	}
}
