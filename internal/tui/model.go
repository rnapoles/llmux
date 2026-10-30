package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"

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
	frame    int

	detail    []string
	dOff      int
	detailKey string
	detailID  uint64
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
	return &model{store: store, info: info, follow: true, wrap: true, dialects: ds, lastVer: ^uint64(0)}
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
		s := all[i].Summary()
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
	m.updateDetail(false)
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

func (m *model) updateDetail(force bool) {
	if m.cursor >= len(m.exs) || len(m.exs) == 0 {
		m.detail, m.detailKey = nil, ""
		return
	}
	ex := m.exs[m.cursor]
	id := m.rows[m.cursor].ID
	key := fmt.Sprintf("%d/%d/%d/%v/%v/%d", id, ex.Rev(), m.tab, m.raw, m.wrap, m.detailW())
	if !force && key == m.detailKey {
		return
	}
	m.detailKey = key
	if id != m.detailID {
		m.detailID, m.dOff = id, 0
	}
	rec := ex.Snapshot()
	if m.info.Redact {
		rec.Client.ReqHeader = inspector.RedactHeaders(rec.Client.ReqHeader)
		rec.Client.RespHeader = inspector.RedactHeaders(rec.Client.RespHeader)
		rec.Upstream.ReqHeader = inspector.RedactHeaders(rec.Upstream.ReqHeader)
		rec.Upstream.RespHeader = inspector.RedactHeaders(rec.Upstream.RespHeader)
	}
	m.detail = buildDetail(rec, m.tab, m.raw, m.wrap, m.detailW()-1)
	m.clampDetail()
}

func (m *model) clampDetail() {
	max := len(m.detail) - m.detailH()
	if max < 0 {
		max = 0
	}
	if m.dOff > max {
		m.dOff = max
	}
	if m.dOff < 0 {
		m.dOff = 0
	}
}

func (m *model) move(delta int) {
	m.cursor += delta
	m.follow = false
	m.clamp()
	m.dOff = 0
	m.updateDetail(false)
}

func (m *model) scrollDetail(delta int) {
	m.dOff += delta
	m.clampDetail()
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clamp()
		m.updateDetail(true)
	case tickMsg:
		m.frame++
		m.refresh(false)
		return m, tick()
	case serverErrMsg:
		m.fatal = msg.err
		return m, tea.Quit
	case tea.KeyMsg:
		return m.onKey(msg)
	case tea.MouseMsg:
		m.onMouse(msg)
	}
	return m, nil
}

func (m *model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		switch msg.Type {
		case tea.KeyEsc, tea.KeyEnter, tea.KeyTab:
			m.focus = paneList
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case tea.KeyCtrlU:
			m.filter = ""
		case tea.KeyRunes, tea.KeySpace:
			m.filter += string(msg.Runes)
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
			m.dOff = 0
		} else {
			m.follow = true
			m.cursor = 0
			m.clamp()
			m.updateDetail(false)
		}
	case "end", "G":
		if m.focus == paneDetail {
			m.dOff = 1 << 30
			m.clampDetail()
		} else {
			m.move(len(m.rows))
		}
	case "K":
		m.scrollDetail(-3)
	case "J":
		m.scrollDetail(3)
	case " ":
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
			m.updateDetail(false)
		}
	}
	return m, nil
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
	m.tab, m.dOff = t, 0
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
	m.detailKey = ""
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
			m.updateDetail(false)
		}
	case a == "help":
		m.showHelp = true
	case a == "dialect":
		m.cycleDialect()
	case strings.HasPrefix(a, "sf:"):
		m.setStatusFilter(statusFilter(a[3] - '0'))
	}
}

func (m *model) onMouse(msg tea.MouseMsg) {
	if m.showHelp {
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			m.showHelp = false
		}
		return
	}
	inList := msg.X < m.leftW()
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		d := 1
		if msg.Button == tea.MouseButtonWheelUp {
			d = -1
		}
		if inList {
			m.move(d)
		} else {
			m.scrollDetail(3 * d)
		}
		return
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return
		}
	default:
		return
	}

	switch {
	case msg.Y == 1: // toolbar
		_, hits := m.toolbar()
		for _, h := range hits {
			if msg.X >= h.x0 && msg.X < h.x1 {
				m.act(h.act)
				return
			}
		}
	case inList && msg.Y >= 4 && msg.Y < 2+m.panelH()-1:
		m.focus = paneList
		idx := m.offset + (msg.Y-4)/2
		if idx < len(m.rows) {
			m.cursor = idx
			m.follow = false
			m.dOff = 0
			m.updateDetail(false)
		}
	case !inList && msg.Y == 3: // tab strip
		m.focus = paneDetail
		for i, h := range m.tabHits() {
			if msg.X >= h.x0 && msg.X < h.x1 {
				m.setTab(tabID(i))
				return
			}
		}
	case !inList && msg.Y > 3:
		m.focus = paneDetail
	}
}
