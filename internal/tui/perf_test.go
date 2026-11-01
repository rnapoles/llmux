package tui

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rnapoles/llmux/internal/inspector"
)

// bigModel holds n exchanges whose bodies are long LLM-style texts.
func bigModel(n, kb int) *model {
	store := inspector.NewStore(1000, 8<<20)
	para := "Lorem ipsum dolor sit amet, consectetur adipiscing elit — café 日本語のテキスト 😀 ✔ sed do eiusmod tempor. "
	text := strings.Repeat(para, kb*1024/len(para)+1)
	for i := 0; i < n; i++ {
		ex := store.Begin()
		ex.Update(func(r *inspector.Record) {
			r.Client.Method, r.Client.URL, r.Model, r.Dialect, r.Mode = "POST", "/v1/chat/completions", "gpt-4o", "openai", "proxy"
			r.Client.Status, r.Done, r.Finished = 200, true, r.Started.Add(time.Second)
			r.Client.RespHeader = http.Header{"Content-Type": {"application/json"}}
		})
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.ReqBody },
			[]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"`+text[:2000]+`"}]}`))
		ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody },
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"`+text+`"}}]}`))
	}
	m := newModel(store, Info{Version: "t", Listen: ":1", Hosts: []HostInfo{{Dialects: []string{"openai"}}}})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.refresh(true)
	m.setTab(tabResponse)
	return m
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000) }

func TestPerfReport(t *testing.T) {
	for _, kb := range []int{64, 512} {
		m := bigModel(20, kb)
		rec := cleanRecord(m.exs[0].Snapshot())

		t0 := time.Now()
		_ = m.exs[0].Snapshot()
		snap := time.Since(t0)

		t0 = time.Now()
		lines := buildDetail(rec, tabResponse, false, true, m.detailW()-1)
		build := time.Since(t0)

		t0 = time.Now()
		m.updateDetail(true)
		upd := time.Since(t0)

		m.focus = paneDetail
		t0 = time.Now()
		for i := 0; i < 50; i++ {
			m.dOff = i * 3
			_ = m.View()
		}
		view := time.Since(t0) / 50

		m.focus = paneList
		t0 = time.Now()
		for i := 0; i < 10; i++ {
			m.move(1)
		}
		move := time.Since(t0) / 10

		t.Logf("body=%4dKB lines=%6d | snapshot %7s | buildDetail %8s | updateDetail %8s | View/scroll step %7s | list selection change %8s",
			kb, len(lines), ms(snap), ms(build), ms(upd), ms(view), ms(move))
	}
}

func TestFinishedViewsAreCached(t *testing.T) {
	m := bigModel(3, 64)
	m.lastBuild = 0 // cheap: selection changes build immediately
	m.cursor = 0
	m.updateDetail(true)
	first := m.detail
	m.move(1)
	if &m.detail[0] == &first[0] {
		t.Fatal("selection change must show the other exchange")
	}
	m.move(-1) // back to the first one
	if &m.detail[0] != &first[0] {
		t.Error("revisiting a finished exchange should be served from the cache, not rebuilt")
	}
	if len(m.cache) < 2 {
		t.Errorf("cache holds %d entries", len(m.cache))
	}
	// the cache is bounded
	for i := 0; i < cacheEntries+10; i++ {
		m.remember(fmt.Sprint("k", i), []string{"x"})
	}
	if len(m.cache) > cacheEntries {
		t.Errorf("cache grew to %d entries", len(m.cache))
	}
	// and a tab switch is a different view
	before := m.detailKey
	m.setTab(tabOverview)
	if m.detailKey == before {
		t.Error("tab switch must change the view key")
	}
}

func TestHeavySelectionChangesAreDebounced(t *testing.T) {
	m := bigModel(6, 64)
	m.updateDetail(true)
	shown := m.detailID
	m.lastBuild = time.Second // pretend the last render was heavy
	m.cache, m.cacheOrder = map[string][]string{}, nil

	for i := 0; i < 5; i++ { // a held arrow key
		m.move(1)
	}
	if !m.stale {
		t.Fatal("heavy navigation should defer the render")
	}
	if m.detailID != shown {
		t.Fatal("nothing may be built while the selection is still moving")
	}
	if m.cmd == nil {
		t.Fatal("a debounce timer should have been scheduled")
	}
	if !strings.Contains(ansiRe.ReplaceAllString(m.View(), ""), "rendering") {
		t.Error("a placeholder should be shown while the render is pending")
	}

	// timers from earlier key presses are obsolete; only the last one builds
	old := m.navSeq - 1
	m.Update(buildMsg{seq: old})
	if !m.stale {
		t.Fatal("a stale timer must be ignored")
	}
	m.Update(buildMsg{seq: m.navSeq})
	if m.stale || m.detailID != m.rows[m.cursor].ID {
		t.Fatalf("latest timer must render the settled selection (stale=%v)", m.stale)
	}
	if strings.Contains(ansiRe.ReplaceAllString(m.View(), ""), "rendering large") {
		t.Error("placeholder should be gone after the render")
	}
}

func TestLiveExchangesAreRebuiltAtMostFewTimesPerSecond(t *testing.T) {
	store := inspector.NewStore(10, 1<<20)
	ex := store.Begin()
	ex.Update(func(r *inspector.Record) {
		r.Client.Method, r.Client.URL, r.Dialect = "POST", "/v1/chat/completions", "openai"
		r.Client.Status = 200
	})
	m := newModel(store, Info{Version: "t", Listen: ":1", Hosts: []HostInfo{{Dialects: []string{"openai"}}}})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m.setTab(tabResponse)
	m.refresh(true)
	m.updateDetail(true)
	first := m.detailKey

	ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody }, []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n"))
	m.refresh(true)
	if m.detailKey != first {
		t.Error("a live exchange must not be re-rendered on every chunk")
	}
	m.builtAt = time.Now().Add(-time.Second)
	m.updateDetail(false)
	if m.detailKey == first {
		t.Error("after the throttle window the new data must show up")
	}
	if len(m.cache) != 0 {
		t.Error("unfinished exchanges must not be cached")
	}
}

func TestSnapshotLimitCapsCopiedBytes(t *testing.T) {
	store := inspector.NewStore(10, 1<<20)
	ex := store.Begin()
	ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody }, []byte(strings.Repeat("x", 5000)))
	snap := ex.SnapshotLimit(100)
	if len(snap.Client.RespBody.Data) != 100 || snap.Client.RespBody.Total != 5000 {
		t.Errorf("data=%d total=%d", len(snap.Client.RespBody.Data), snap.Client.RespBody.Total)
	}
	if full := ex.Snapshot(); len(full.Client.RespBody.Data) != 5000 {
		t.Errorf("Snapshot must stay complete: %d", len(full.Client.RespBody.Data))
	}
	out := renderBody(snap.Client.RespBody, true)
	if !strings.Contains(out, "x") {
		t.Error("capped body still renders")
	}
}

func TestCoalescedKeyRepeatsAreNotLost(t *testing.T) {
	m := bigModel(8, 8)
	m.lastBuild = 0
	m.focus = paneList
	start := m.cursor

	// Bubble Tea delivers keys that arrive in the same read as ONE message.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jjjj")})
	if m.cursor != start+4 {
		t.Fatalf("4 queued 'j' presses moved the cursor by %d, want 4", m.cursor-start)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("kk")})
	if m.cursor != start+2 {
		t.Fatalf("cursor at %d, want %d", m.cursor, start+2)
	}

	// Pasted text must never fire shortcuts (a stray "q" would quit the app).
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted quit text"), Paste: true})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("pasted text quit the application")
		}
	}
	if m.paused || m.showHelp {
		t.Error("pasted text triggered shortcuts")
	}

	// ...but it is welcome in the filter box.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("gpt-4o"), Paste: true})
	if m.filter != "gpt-4o" {
		t.Errorf("filter = %q", m.filter)
	}
}
