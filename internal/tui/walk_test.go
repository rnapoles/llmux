package tui

import (
	"fmt"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rnapoles/llmux/internal/inspector"
)

func walkText(rng *rand.Rand, n int) string {
	parts := []string{"lorem ipsum ", "日本語のテキスト", "😀", "👍🏽", "café ", "x", strings.Repeat("A", 90), "\\n", "\n", "\t", "{\"a\":1}", " ", "…", "┃", "⚠️", "🇺🇸", "ñ", "\u00a0"}
	var sb strings.Builder
	for sb.Len() < n {
		sb.WriteString(parts[rng.Intn(len(parts))])
	}
	return sb.String()
}

func addExchange(rng *rand.Rand, store *inspector.Store) *inspector.Exchange {
	ex := store.Begin()
	size := []int{50, 700, 3000, 3100, 9000, 20000}[rng.Intn(6)]
	stream := rng.Intn(3) == 0
	ex.Update(func(r *inspector.Record) {
		r.Client.Method, r.Client.URL = "POST", "/v1/chat/completions?"+walkText(rng, rng.Intn(400))
		r.Model, r.Dialect, r.Mode, r.Stream = walkText(rng, rng.Intn(30)), "openai", []string{"proxy", "translate"}[rng.Intn(2)], stream
		r.Client.ReqHeader = http.Header{"Cookie": {walkText(rng, size)}, "X-A": {walkText(rng, 20)}}
		r.Upstream.Method, r.Upstream.URL = "POST", "http://u/"+walkText(rng, rng.Intn(100))
	})
	body := `{"model":"m","messages":[{"role":"user","content":"` + strings.ReplaceAll(walkText(rng, size), `"`, `\"`) + `"}]}`
	ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.ReqBody }, []byte(body))
	if rng.Intn(4) != 0 {
		finish(rng, ex, size, stream)
	}
	return ex
}

func finish(rng *rand.Rand, ex *inspector.Exchange, size int, stream bool) {
	resp := `{"choices":[{"message":{"role":"assistant","content":"` + strings.ReplaceAll(walkText(rng, size), `"`, `\"`) + `"}}]}`
	if stream {
		var sb strings.Builder
		for _, w := range strings.Fields(walkText(rng, size)) {
			sb.WriteString(`data: {"choices":[{"delta":{"content":"` + strings.ReplaceAll(w, `"`, `\"`) + `"}}]}` + "\n\n")
		}
		resp = sb.String() + "data: [DONE]\n\n"
	}
	ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody }, []byte(resp))
	ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Upstream.RespBody }, []byte(resp))
	ex.Update(func(r *inspector.Record) {
		r.Client.Status, r.Upstream.Status, r.Done, r.Finished = 200, 200, true, r.Started.Add(time.Second)
		r.Client.RespHeader = http.Header{"Content-Type": {"application/json"}}
	})
}

var statusRe = regexp.MustCompile(`item \d+/\d+\s+scroll \d+%`)

func checkFrame(t *testing.T, m *model, label string) {
	t.Helper()
	out := m.render()
	lines := strings.Split(out, "\n")
	if m.w < 60 || m.h < 12 {
		return
	}
	if len(lines) != m.h {
		t.Fatalf("%s: frame has %d lines for a %dx%d terminal", label, len(lines), m.w, m.h)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != m.w {
			t.Fatalf("%s: line %d is %d cells wide (terminal %d)\n%s", label, i, w, m.w, ansiRe.ReplaceAllString(l, ""))
		}
	}
	// only the renderer's own sequences may reach the terminal
	plain := ansiKeep.ReplaceAllString(out, "")
	for _, r := range plain {
		if r != '\n' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)) {
			t.Fatalf("%s: control character %U reached the terminal", label, r)
		}
	}
	if len([]rune(m.filter)) > maxFilterRunes {
		t.Fatalf("%s: filter grew to %d runes", label, len([]rune(m.filter)))
	}
	if !strings.Contains(ansiRe.ReplaceAllString(lines[0], ""), "LLMUX") {
		t.Fatalf("%s: header missing", label)
	}
	if n := len(statusRe.FindAllString(ansiRe.ReplaceAllString(out, ""), -1)); n != 1 {
		t.Fatalf("%s: status bar rendered %d times", label, n)
	}
}

func TestRandomWalkKeepsFrameValid(t *testing.T) {
	keys := []string{"j", "k", "J", "K", "g", "G", " ", "b", "tab", "shift+tab", "enter", "left", "right", "h", "l", "1", "2", "3", "4", "5", "space", "backspace", "ctrl+u",
		"s", "d", "p", "w", "r", "f", "c", "?", "esc", "/", "x", "pgup", "pgdown", "home", "end", "up", "down", "[", "]"}
	seeds, steps := int64(8), 450
	if raceEnabled { // the race detector is ~10x slower; the full walk runs without it
		seeds, steps = 2, 250
	}
	for seed := int64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewSource(seed))
		store := inspector.NewStore(60, 4<<20)
		m := newModel(store, Info{Version: "t", Listen: "127.0.0.1:1", Persist: "x.db", Redact: seed%2 == 0,
			Hosts: []HostInfo{{Dialects: []string{"openai", "ollama"}}}})
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
		var live []*inspector.Exchange
		for step := 0; step < steps; step++ {
			label := fmt.Sprintf("seed=%d step=%d", seed, step)
			switch op := rng.Intn(12); {
			case op < 6:
				k := keys[rng.Intn(len(keys))]
				m.Update(kp(k))
				if m.focus == paneFilter && rng.Intn(2) == 0 {
					hostile := "\x1b[2J\x1b[H\x1b[?1049l\x1b]0;pwned\x07\x00\r\n\u202e"
					m.Update(paste(hostile + walkText(rng, 3000)))
				}
			case op == 6:
				m.Update(tea.WindowSizeMsg{Width: 60 + rng.Intn(150), Height: 12 + rng.Intn(50)})
			case op == 7:
				live = append(live, addExchange(rng, store))
			case op == 8 && len(live) > 0:
				ex := live[rng.Intn(len(live))]
				ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody }, []byte("data: {\"choices\":[{\"delta\":{\"content\":\""+walkText(rng, 200)+"\"}}]}\n\n"))
			case op == 9:
				m.Update(click(rng.Intn(m.w), rng.Intn(m.h)))
			case op == 10:
				m.Update(wheel(rng.Intn(m.w), rng.Intn(m.h), rng.Intn(2) == 0))
			default:
				m.Update(tickMsg(time.Now()))
			}
			if c := m.takeCmd(); c != nil && rng.Intn(2) == 0 { // deliver the debounce timer sometimes
				if bm, ok := c().(buildMsg); ok {
					m.Update(bm)
				}
			}
			checkFrame(t, m, label)
		}
	}
}
