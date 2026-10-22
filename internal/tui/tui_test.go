package tui

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rnapoles/llmux/internal/inspector"
)

func seed(store *inspector.Store) {
	mk := func(method, path, model, dialect, mode string, status int, req, resp string, stream bool) {
		ex := store.Begin()
		ex.Update(func(r *inspector.Record) {
			r.Client.Method, r.Client.URL, r.Model, r.Dialect, r.Mode = method, path, model, dialect, mode
			r.Backend, r.VHost, r.Host, r.ClientAddr = "openai", "main", "localhost:8080", "127.0.0.1:5000"
			r.Client.ReqHeader = http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer sk-a••••b"}}
			r.Client.RespHeader = http.Header{"Content-Type": {"application/json"}}
			r.Client.Status, r.Stream, r.Operation = status, stream, "chat"
			r.Prompt, r.Completion = 12, 34
			r.Done, r.Finished = true, r.Started.Add(1234*time.Millisecond)
		})
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.ReqBody }, []byte(req))
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody }, []byte(resp))
	}
	mk("POST", "/v1/chat/completions", "gpt-4o", "openai", "proxy", 200,
		`{"model":"gpt-4o","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Say hello"}],"temperature":0.7,"stream":false}`,
		`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there!\nHow can I help?"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":34}}`, false)
	mk("POST", "/api/chat", "llama3", "ollama", "translate", 200, `{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`,
		"{\"message\":{\"content\":\"He\"},\"done\":false}\n{\"message\":{\"content\":\"llo\"},\"done\":false}\n{\"done\":true,\"eval_count\":2}\n", true)
	mk("GET", "/v1/models", "", "openai", "proxy", 404, ``, `{"error":{"message":"nope"}}`, false)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestRender(t *testing.T) {
	store := inspector.NewStore(100, 1<<20)
	seed(store)
	m := newModel(store, Info{Version: "test", Listen: "127.0.0.1:8080", Hosts: []HostInfo{{Name: "main", Dialects: []string{"openai", "ollama"}}}})
	for _, size := range [][2]int{{140, 40}, {100, 30}, {70, 16}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.refresh(true)
		out := m.View()
		lines := strings.Split(out, "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: got %d lines", size, len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != size[0] {
				t.Errorf("%v: line %d width %d\n%s", size, i, w, ansiRe.ReplaceAllString(l, ""))
				break
			}
		}
		if size[0] == 140 && os.Getenv("SHOW") != "" {
			show := func(n int) {
				for _, l := range strings.Split(ansiRe.ReplaceAllString(m.View(), ""), "\n")[:n] {
					println(strings.TrimRight(l, " "))
				}
			}
			m.cursor = 2
			m.follow = false
			m.setTab(tabOverview)
			show(30)
			m.cursor = 1
			m.setTab(tabResponse)
			show(24)
			m.showHelp = true
			show(40)
			m.showHelp = false
		}
		if size[0] == 70 && os.Getenv("SHOW") != "" {
			println(ansiRe.ReplaceAllString(m.View(), ""))
		}
	}
	// keys & mouse must not panic
	for _, k := range []string{"j", "k", "tab", "down", "G", "g", "s", "s", "s", "d", "p", "p", "w", "r", "f", "?", "esc", "1", "5", "/", "x", "esc", "c"} {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	}
	m.Update(tea.MouseMsg{X: 5, Y: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	_ = m.View()
}
