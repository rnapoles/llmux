package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/rnapoles/llmux/internal/inspector"
)

type pane int

const (
	paneList pane = iota
	paneDetail
	paneFilter
)

type statusFilter int

const (
	sfAll statusFilter = iota
	sfOK
	sfErr
	sfStream
	sfCount
)

var statusFilterNames = [sfCount]string{"ALL", "2xx", "ERR", "STREAM"}

type (
	tickMsg      time.Time
	serverErrMsg struct{ err error }
	buildMsg     struct{ seq int } // debounce timer for a heavy detail build
)

const (
	cacheEntries   = 24
	cheapBuild     = 12 * time.Millisecond // builds faster than this run immediately
	buildDebounce  = 70 * time.Millisecond
	liveRebuildGap = 250 * time.Millisecond
)

type stats struct {
	total, errors, tokens int
	avg                   time.Duration
}

type hit struct {
	x0, x1 int // [x0, x1)
	act    string
}

type model struct {
	store *inspector.Store
	info  Info
	fatal error

	w, h int

	rows    []inspector.Summary
	exs     []*inspector.Exchange
	cursor  int
	offset  int
	stats   stats
	lastVer uint64

	focus    pane
	filter   string
	sf       statusFilter
	dialect  string // "" = all
	dialects []string
	paused   bool
	follow   bool
	wrap     bool
	raw      bool
	showHelp bool
	tab      tabID
	spin     int // spinner frame

	detail    []string       // rendered lines of the selected view (also the cache payload)
	vp        viewport.Model // owns scrolling and clipping of m.detail
	detailKey string         // identity of what m.detail shows, including the exchange revision
	viewKey   string         // same, without the revision (to spot "same view, new data")
	detailID  uint64

	// Rendering performance (see updateDetail / requestDetail).
	cache      map[string][]string // finished exchanges only
	cacheOrder []string
	builtAt    time.Time
	lastBuild  time.Duration
	stale      bool // a deferred build is pending: show a placeholder
	navSeq     int
	cmd        tea.Cmd
}

func newModel(store *inspector.Store, info Info) *model {
	seen := map[string]bool{}
	var ds []string
	for _, h := range info.Hosts {
		for _, d := range h.Dialects {
			if !seen[d] {
				seen[d] = true
				ds = append(ds, d)
			}
		}
	}
	return &model{store: store, info: info, follow: true, wrap: true, dialects: ds, lastVer: ^uint64(0), cache: map[string][]string{}, vp: viewport.New()}
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Init() tea.Cmd { return tick() }

// ---------------------------------------------------------------------------
// Layout metrics (shared by View and mouse hit-testing)
// ---------------------------------------------------------------------------

func (m *model) leftW() int {
	w := m.w * 42 / 100
	if w < 40 {
		w = 40
	}
	if w > m.w-30 {
		w = m.w - 30
	}
	return w
}

func (m *model) panelH() int { return m.h - 3 }

// list geometry: inner lines = panelH-2; line 0 is the title; 2 lines per item.
func (m *model) pageItems() int {
	n := (m.panelH() - 2 - 1) / 2
	if n < 1 {
		n = 1
	}
	return n
}

func (m *model) detailW() int { return m.w - m.leftW() - 2 }
func (m *model) detailH() int {
	h := m.panelH() - 2 - 1 // minus borders and tab line
	if h < 1 {
		h = 1
	}
	return h
}

// ---------------------------------------------------------------------------
// Data
// ---------------------------------------------------------------------------

func (m *model) match(s inspector.Summary) bool {
	switch m.sf {
	case sfOK:
		if s.Status < 200 || s.Status >= 400 || s.Error {
			return false
		}
	case sfErr:
		if !s.Error {
			return false
		}
	case sfStream:
		if !s.Stream {
			return false
		}
	}
	if m.dialect != "" && s.Dialect != m.dialect {
		return false
	}
	for _, term := range strings.Fields(strings.ToLower(m.filter)) {
		if !strings.Contains(s.Search, term) {
			return false
		}
	}
	return true
}

func (m *model) selectedID() uint64 {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return m.rows[m.cursor].ID
	}
	return 0
}

func (m *model) refresh(force bool) {
	if m.store == nil {
		return
	}
	v := m.store.Version()
	if !force && (m.paused || v == m.lastVer) {
		return
	}
	m.lastVer = v
	prev := m.selectedID()
	all := m.store.List()
	var st stats
	var durSum time.Duration
	var done int
	rows := make([]inspector.Summary, 0, len(all))
	exs := make([]*inspector.Exchange, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- { // newest first
		s := cleanSummary(all[i].Summary())
		st.total++
		if s.Error {
			st.errors++
		}
		st.tokens += s.Prompt + s.Completion
		if s.Done {
			done++
			durSum += s.Duration
		}
		if m.match(s) {
			rows = append(rows, s)
			exs = append(exs, all[i])
		}
	}
	if done > 0 {
		st.avg = durSum / time.Duration(done)
	}
	m.stats, m.rows, m.exs = st, rows, exs

	switch {
	case m.follow:
		m.cursor = 0
	default:
		m.cursor = 0
		for i, s := range rows {
			if s.ID == prev {
				m.cursor = i
				break
			}
		}
	}
	m.clamp()
	if m.selectedID() != m.detailID {
		m.requestDetail()
	} else {
		m.updateDetail(false)
	}
}

func (m *model) clamp() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	page := m.pageItems()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+page {
		m.offset = m.cursor - page + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// keys identify the detail view of row i: view ignores the exchange revision.
func (m *model) keys(i int) (view, key string) {
	view = fmt.Sprintf("%d/%d/%v/%v/%d/%v", m.rows[i].ID, m.tab, m.raw, m.wrap, m.detailW(), m.info.Redact)
	return view, fmt.Sprintf("%s/%d", view, m.exs[i].Rev())
}

func (m *model) remember(key string, lines []string) {
	if _, ok := m.cache[key]; !ok {
		m.cacheOrder = append(m.cacheOrder, key)
		if len(m.cacheOrder) > cacheEntries {
			delete(m.cache, m.cacheOrder[0])
			m.cacheOrder = m.cacheOrder[1:]
		}
	}
	m.cache[key] = lines
}

func (m *model) dropCache() {
	m.cache, m.cacheOrder = map[string][]string{}, nil
	m.detailKey, m.viewKey = "", ""
}

// updateDetail brings m.detail in line with the selected row.
//
//   - unchanged view: nothing to do;
//   - finished exchange seen before: served from the cache;
//   - live exchange: re-rendered at most a few times per second;
//   - otherwise rendered now (force bypasses the throttles).
func (m *model) updateDetail(force bool) {
	if len(m.exs) == 0 || m.cursor >= len(m.exs) {
		m.detailKey, m.viewKey, m.stale = "", "", false
		m.setDetail(nil)
		return
	}
	if m.stale && !force {
		return // a debounced build is on its way
	}
	view, key := m.keys(m.cursor)
	id := m.rows[m.cursor].ID
	if !force && key == m.detailKey {
		return
	}
	if id != m.detailID {
		m.detailID = id
		m.vp.GotoTop()
	}
	if lines, ok := m.cache[key]; ok {
		m.detailKey, m.viewKey, m.stale = key, view, false
		m.setDetail(lines)
		return
	}
	done := m.rows[m.cursor].Done
	if !force && !done && view == m.viewKey && time.Since(m.builtAt) < liveRebuildGap {
		return
	}

	t0 := time.Now()
	rec := cleanRecord(m.exs[m.cursor].SnapshotLimit(displayLimit))
	if m.info.Redact {
		rec.Client.ReqHeader = inspector.RedactHeaders(rec.Client.ReqHeader)
		rec.Client.RespHeader = inspector.RedactHeaders(rec.Client.RespHeader)
		rec.Upstream.ReqHeader = inspector.RedactHeaders(rec.Upstream.ReqHeader)
		rec.Upstream.RespHeader = inspector.RedactHeaders(rec.Upstream.RespHeader)
	}
	lines := buildDetail(rec, m.tab, m.raw, m.wrap, m.detailW()-1)
	m.lastBuild, m.builtAt = time.Since(t0), time.Now()
	m.detailKey, m.viewKey, m.stale = key, view, false
	m.setDetail(lines)
	if done {
		m.remember(key, lines)
	}
}

// requestDetail is updateDetail for selection changes. Cheap or cached views
// appear instantly; when the last build was heavy (long texts), rapid
// navigation (a held arrow key) shows a placeholder and renders only once the
// selection has settled, instead of queueing one slow build per key repeat.
func (m *model) requestDetail() {
	if len(m.exs) == 0 || m.cursor >= len(m.exs) {
		m.updateDetail(true)
		return
	}
	_, key := m.keys(m.cursor)
	if _, hit := m.cache[key]; hit || m.lastBuild < cheapBuild {
		m.stale = false
		m.updateDetail(false)
		return
	}
	m.stale = true
	m.navSeq++
	seq := m.navSeq
	m.cmd = tea.Tick(buildDebounce, func(time.Time) tea.Msg { return buildMsg{seq} })
}

func (m *model) takeCmd() tea.Cmd {
	c := m.cmd
	m.cmd = nil
	return c
}

// syncVP keeps the viewport the size of the detail text area.
func (m *model) syncVP() {
	m.vp.SetWidth(max(1, m.detailW()-1))
	m.vp.SetHeight(m.detailH())
}

// setDetail installs new content; the viewport keeps (and clamps) its offset.
func (m *model) setDetail(lines []string) {
	m.detail = lines
	m.syncVP()
	m.vp.SetContentLines(lines)
}

func (m *model) clampDetail() { m.vp.SetYOffset(m.vp.YOffset()) } // re-clamp after a size change

func (m *model) move(delta int) {
	m.cursor += delta
	m.follow = false
	m.clamp()
	m.vp.GotoTop()
	m.requestDetail()
}

func (m *model) scrollDetail(delta int) {
	if delta < 0 {
		m.vp.ScrollUp(-delta)
	} else {
		m.vp.ScrollDown(delta)
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.syncVP()
		m.clamp()
		m.updateDetail(true)
	case tickMsg:
		m.spin++
		m.refresh(false)
		return m, tea.Batch(tick(), m.takeCmd())
	case buildMsg:
		if msg.seq == m.navSeq && m.stale {
			m.updateDetail(true)
		}
	case serverErrMsg:
		m.fatal = msg.err
		return m, tea.Quit
	case tea.KeyPressMsg:
		mm, cmd := m.onKey(msg)
		return mm, tea.Batch(cmd, m.takeCmd())
	case tea.PasteMsg:
		m.onPaste(msg.Content)
	case tea.MouseClickMsg:
		m.onClick(tea.Mouse(msg))
	case tea.MouseWheelMsg:
		m.onWheel(tea.Mouse(msg))
	}
	return m, m.takeCmd()
}

// onPaste handles bracketed paste. Pasted text is untrusted: it only ever goes
// into the filter box (cleaned and bounded by typeFilter); anywhere else it is
// ignored, so it can never trigger shortcuts (a stray "q" would quit).
func (m *model) onPaste(text string) {
	if m.focus != paneFilter || m.showHelp {
		return
	}
	m.typeFilter(text)
	m.follow = true
	m.refresh(true)
}

func (m *model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := msg.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	if m.showHelp {
		switch s {
		case "esc", "?", "q", "enter":
			m.showHelp = false
		}
		return m, nil
	}
	if m.focus == paneFilter {
		switch s {
		case "esc", "enter", "tab":
			m.focus = paneList
		case "backspace":
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case "ctrl+u":
			m.filter = ""
		default:
			if k := msg.Key(); k.Text != "" { // printable text (a space has Text " ")
				m.typeFilter(k.Text)
			}
		}
		m.follow = true
		m.refresh(true)
		return m, nil
	}

	switch s {
	case "q":
		return m, tea.Quit
	case "?", "f1":
		m.showHelp = true
	case "/":
		m.focus = paneFilter
	case "tab":
		if m.focus == paneList {
			m.focus = paneDetail
		} else {
			m.focus = paneList
		}
	case "shift+tab":
		if m.focus == paneDetail {
			m.focus = paneList
		} else {
			m.focus = paneDetail
		}
	case "enter", "right", "l":
		if s == "enter" && m.focus == paneList {
			m.focus = paneDetail
		} else {
			m.setTab(m.tab + 1)
		}
	case "left", "h":
		m.setTab(m.tab - 1)
	case "]":
		m.setTab(m.tab + 1)
	case "[":
		m.setTab(m.tab - 1)
	case "esc":
		switch {
		case m.focus == paneDetail:
			m.focus = paneList
		case m.filter != "" || m.sf != sfAll || m.dialect != "":
			m.filter, m.sf, m.dialect = "", sfAll, ""
			m.refresh(true)
		}
	case "1", "2", "3", "4", "5":
		m.setTab(tabID(s[0] - '1'))
	case "up", "k":
		m.navigate(-1)
	case "down", "j":
		m.navigate(1)
	case "pgup", "ctrl+u":
		m.navigate(-m.page())
	case "pgdown", "ctrl+d":
		m.navigate(m.page())
	case "home", "g":
		if m.focus == paneDetail {
			m.vp.GotoTop()
		} else {
			m.follow = true
			m.cursor = 0
			m.clamp()
			m.requestDetail()
		}
	case "end", "G":
		if m.focus == paneDetail {
			m.vp.GotoBottom()
		} else {
			m.move(len(m.rows))
		}
	case "K":
		m.scrollDetail(-3)
	case "J":
		m.scrollDetail(3)
	case "space":
		m.scrollDetail(m.detailH() - 1)
	case "b":
		m.scrollDetail(-(m.detailH() - 1))
	case "s":
		m.setStatusFilter((m.sf + 1) % sfCount)
	case "d":
		m.cycleDialect()
	case "p":
		m.togglePause()
	case "c":
		m.clear()
	case "w":
		m.wrap = !m.wrap
		m.updateDetail(true)
	case "r":
		m.raw = !m.raw
		m.updateDetail(true)
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.cursor = 0
			m.clamp()
			m.requestDetail()
		}
	}
	return m, nil
}

// maxFilterRunes bounds the filter text: pasting a whole document must not be
// able to grow state (or the toolbar) without limit.
const maxFilterRunes = 200

// typeFilter appends typed or pasted text to the filter. Pasted text is
// untrusted terminal input: control characters, escape sequences and line
// breaks are neutralised (a newline would split the toolbar into several rows
// and tear the layout apart; an escape sequence could drive the terminal).
func (m *model) typeFilter(s string) {
	r := []rune(m.filter + inline(s))
	if len(r) > maxFilterRunes {
		r = r[:maxFilterRunes]
	}
	m.filter = string(r)
}

func (m *model) page() int {
	if m.focus == paneDetail {
		return m.detailH() / 2
	}
	return m.pageItems()
}

func (m *model) navigate(delta int) {
	if m.focus == paneDetail {
		m.scrollDetail(delta)
		return
	}
	m.move(delta)
}

func (m *model) setTab(t tabID) {
	if t < 0 {
		t = tabCount - 1
	}
	if t >= tabCount {
		t = 0
	}
	m.tab = t
	m.vp.GotoTop()
	m.updateDetail(true)
}

func (m *model) setStatusFilter(f statusFilter) {
	m.sf = f
	m.follow = true
	m.refresh(true)
}

func (m *model) cycleDialect() {
	opts := append([]string{""}, m.dialects...)
	for i, o := range opts {
		if o == m.dialect {
			m.dialect = opts[(i+1)%len(opts)]
			break
		}
	}
	m.follow = true
	m.refresh(true)
}

func (m *model) togglePause() {
	m.paused = !m.paused
	if !m.paused {
		m.refresh(true)
	}
}

func (m *model) clear() {
	m.store.Clear()
	m.rows, m.exs, m.detail, m.cursor, m.offset = nil, nil, nil, 0, 0
	m.dropCache()
	m.stats = stats{}
	m.follow = true
	m.refresh(true)
}

// act runs a toolbar action by name.
func (m *model) act(a string) {
	switch {
	case a == "filter":
		m.focus = paneFilter
	case a == "pause":
		m.togglePause()
	case a == "clear":
		m.clear()
	case a == "wrap":
		m.wrap = !m.wrap
		m.updateDetail(true)
	case a == "raw":
		m.raw = !m.raw
		m.updateDetail(true)
	case a == "follow":
		m.follow = !m.follow
		if m.follow {
			m.cursor = 0
			m.clamp()
			m.requestDetail()
		}
	case a == "help":
		m.showHelp = true
	case a == "dialect":
		m.cycleDialect()
	case strings.HasPrefix(a, "sf:"):
		m.setStatusFilter(statusFilter(a[3] - '0'))
	}
}

func (m *model) onWheel(ms tea.Mouse) {
	if m.showHelp {
		return
	}
	d := 1
	if ms.Button == tea.MouseWheelUp {
		d = -1
	}
	if ms.X < m.leftW() {
		m.move(d)
	} else {
		m.scrollDetail(3 * d)
	}
}

func (m *model) onClick(ms tea.Mouse) {
	if m.showHelp {
		if ms.Button == tea.MouseLeft {
			m.showHelp = false
		}
		return
	}
	if ms.Button != tea.MouseLeft {
		return
	}
	inList := ms.X < m.leftW()
	switch {
	case ms.Y == 1: // toolbar
		_, hits := m.toolbar()
		for _, h := range hits {
			if ms.X >= h.x0 && ms.X < h.x1 {
				m.act(h.act)
				return
			}
		}
	case inList && ms.Y >= 4 && ms.Y < 2+m.panelH()-1:
		m.focus = paneList
		idx := m.offset + (ms.Y-4)/2
		if idx < len(m.rows) {
			m.cursor = idx
			m.follow = false
			m.vp.GotoTop()
			m.requestDetail()
		}
	case !inList && ms.Y == 3: // tab strip
		m.focus = paneDetail
		for i, h := range m.tabHits() {
			if ms.X >= h.x0 && ms.X < h.x1 {
				m.setTab(tabID(i))
				return
			}
		}
	case !inList && ms.Y > 3:
		m.focus = paneDetail
	}
}
