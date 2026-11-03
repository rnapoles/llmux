package tui

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
		out := m.render()
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
				for _, l := range strings.Split(ansiRe.ReplaceAllString(m.render(), ""), "\n")[:n] {
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
			println(ansiRe.ReplaceAllString(m.render(), ""))
		}
	}
	// keys & mouse must not panic
	for _, k := range []string{"j", "k", "tab", "down", "G", "g", "s", "s", "s", "d", "p", "p", "w", "r", "f", "?", "esc", "1", "5", "/", "x", "esc", "c"} {
		m.Update(kp(k))
	}
	m.Update(click(5, 1))
	m.Update(click(5, 5))
	_ = m.render()
}

var ansiKeep = regexp.MustCompile(`\x1b\[[0-9;]*m`) // the only escape sequences content may contain (colours)

func TestCleanNormalisesAmbiguousText(t *testing.T) {
	cases := map[string]string{
		"a\x1b[2Jb":          "a[2Jb",         // ESC dropped
		"fam👨\u200d👩\u200d👧": "fam👨👩👧",        // ZWJ dropped
		"warn\u26a0\ufe0f":   "warn⚠",         // VS16 dropped
		"🇺🇸 and 🇯🇵":          "[US] and [JP]", // flags become text
		"x\u202eevil":        "xevil",         // bidi override
		"a\u2028b":           "ab",            // line separator
		"tab\there":          "tab here",
	}
	for in, want := range cases {
		if got := clean(in, true); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
	if got := inline("a\nb\r\nc"); got != "a b c" {
		t.Errorf("inline newline handling: %q", got)
	}
	if got := sanitize("l1\nl2"); got != "l1\nl2" {
		t.Errorf("sanitize must keep newlines: %q", got)
	}
}

func TestHostileDataNeverBreaksTheFrame(t *testing.T) {
	store := inspector.NewStore(100, 4<<20)
	evil := "evil\n\x1b[2J\x1b[Hmodel 😀👨\u200d👩\u200d👧\u200d👦🇺🇸\u26a0\ufe0f日本語\u202e"
	big := strings.Repeat("😀 👨\u200d👩\u200d👧\u200d👦 日本語のテキスト \u26a0\ufe0f ✔\ufe0f 🇺🇸 café\\n", 400)
	for i := 0; i < 5; i++ {
		ex := store.Begin()
		ex.Update(func(r *inspector.Record) {
			r.Client.Method, r.Client.URL, r.Model, r.Dialect, r.Mode = "POST", "/v1/chat?q=\u200b\u2028x", evil, "openai", "translate"
			r.VHost, r.Backend, r.Host, r.ClientAddr = evil, "openai", evil, "1.2.3.4:5"
			r.Error = "multi\nline\nerror\x1b[31m"
			r.Client.ReqHeader = http.Header{"X-Evil": {evil}}
			r.Client.RespHeader = http.Header{"Content-Type": {"application/json"}}
			r.Upstream.Method, r.Upstream.URL, r.Upstream.Status = "POST", "http://u/"+evil, 200
			r.Client.Status, r.Done, r.Finished, r.Stream = 200, true, r.Started.Add(time.Second), true
		})
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.ReqBody },
			[]byte(`{"model":"`+"m"+`","messages":[{"role":"user\n","content":"`+big+`"}]}`))
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody },
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"`+big+`"}}]}`))
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Upstream.RespBody },
			[]byte("data: {\"choices\":[{\"delta\":{\"content\":\""+big+"\"}}]}\n\ndata: [DONE]\n\n"))
	}

	m := newModel(store, Info{Version: "t", Listen: "127.0.0.1:1", Persist: strings.Repeat("日本語", 20),
		Hosts: []HostInfo{{Name: "h", Dialects: []string{"openai"}}}})
	check := func(label string, w, h int) {
		out := m.render()
		lines := strings.Split(out, "\n")
		if len(lines) != h {
			t.Fatalf("%s: %d lines, want %d", label, len(lines), h)
		}
		for i, l := range lines {
			if got := lipgloss.Width(l); got != w {
				t.Fatalf("%s: line %d is %d cells wide, want %d", label, i, got, w)
			}
		}
		plain := ansiKeep.ReplaceAllString(out, "")
		if strings.ContainsRune(plain, '\x1b') {
			t.Fatalf("%s: stray escape sequence leaked to the terminal", label)
		}
		for _, r := range plain {
			if r != '\n' && unsafeRune(r) {
				t.Fatalf("%s: unsafe rune %U reached the screen", label, r)
			}
			if isRegionalIndicator(r) {
				t.Fatalf("%s: regional indicator reached the screen", label)
			}
		}
	}

	for _, size := range [][2]int{{140, 40}, {100, 30}, {80, 24}, {61, 13}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.refresh(true)
		for _, wrap := range []bool{true, false} {
			m.wrap = wrap
			for tab := tabOverview; tab < tabCount; tab++ {
				m.setTab(tab)
				for i := range m.rows { // every list selection
					m.cursor = i
					m.updateDetail(true)
					check(fmt.Sprintf("%v wrap=%v tab=%d item=%d", size, wrap, tab, i), size[0], size[1])
				}
				m.focus = paneDetail
				for off := 0; off <= len(m.detail)+2; off += 1 + len(m.detail)/25 { // scroll positions across the whole range
					m.vp.SetYOffset(off)
					check(fmt.Sprintf("%v wrap=%v tab=%d off=%d", size, wrap, tab, off), size[0], size[1])
				}
				m.focus = paneList
			}
		}
	}
	m.showHelp = true
	check("help", 61, 13)
}

func TestScrubOnlyLetsColourSequencesThrough(t *testing.T) {
	ok := "\x1b[38;5;81mtext\x1b[0m│"
	if got := scrub(ok); got != ok {
		t.Errorf("colour sequences must pass unchanged: %q", got)
	}
	cases := map[string]string{
		"a\x1b[2Jb":          "a[2Jb",     // clear screen
		"a\x1b[Hb":           "a[Hb",      // cursor home
		"a\x1b[12Gb":         "a[12Gb",    // cursor placement belongs to the renderer
		"a\x1b[?1049lb":      "a[?1049lb", // leave alternate screen
		"a\x1b]0;title\x07b": "a]0;titleb",
		"a\x00\x07\rb\x7fc":  "abc",
		"a\u0085b\u009bc":    "abc", // C1 controls
		"a\x1b[31;1mb":       "a\x1b[31;1mb",
	}
	for in, want := range cases {
		if got := scrub(in); got != want {
			t.Errorf("scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilterInputIsCleanedAndBounded(t *testing.T) {
	m := bigModel(2, 8)
	m.Update(kp("/"))
	m.Update(paste("first line\nsecond\t\x1b[2Jline\r\n"))
	if m.filter != "first line second [2Jline " { // ESC dropped; the leftover text is inert
		t.Errorf("pasted text not neutralised: %q", m.filter)
	}
	m.Update(paste(strings.Repeat("日本語 ", 2000)))
	if n := len([]rune(m.filter)); n != maxFilterRunes {
		t.Errorf("filter has %d runes, want the cap of %d", n, maxFilterRunes)
	}
	// the toolbar stays exactly one row at every width, whatever the filter holds
	for _, w := range []int{60, 80, 140, 200} {
		m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		tb, _ := m.toolbar()
		if strings.Contains(tb, "\n") || lipgloss.Width(tb) != w {
			t.Errorf("width %d: toolbar is %d cells / contains newline: %v", w, lipgloss.Width(tb), strings.Contains(tb, "\n"))
		}
	}
}

func TestCellAwareTruncation(t *testing.T) {
	if got := tailCells("abcdefghij", 6); got != "…fghij" {
		t.Errorf("tailCells = %q", got)
	}
	if got := headCells("abcdefghij", 6); got != "abcde…" {
		t.Errorf("headCells = %q", got)
	}
	for _, s := range []string{"日本語のテキスト", "😀😀😀😀", "mixed 日本 text", ""} {
		for w := 1; w < 14; w++ {
			if lipgloss.Width(headCells(s, w)) > w || lipgloss.Width(tailCells(s, w)) > w {
				t.Errorf("%q does not fit %d cells: %q / %q", s, w, headCells(s, w), tailCells(s, w))
			}
		}
	}
}

func TestViewDeclaresAltScreenAndMouse(t *testing.T) {
	m := bigModel(2, 8)
	v := m.View()
	if !v.AltScreen {
		t.Error("the TUI must run on the alternate screen")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Error("mouse support must be requested through the View")
	}
	if v.Content == "" {
		t.Error("empty frame")
	}
}
