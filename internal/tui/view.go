package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Bar helpers: every fragment carries the bar background so resets inside a
// styled string never punch holes in the bar.
// ---------------------------------------------------------------------------

func on(bg lipgloss.Color, c lipgloss.Color, b bool, s string) string {
	return lipgloss.NewStyle().Foreground(c).Background(bg).Bold(b).Render(s)
}

func gap(bg lipgloss.Color, n int) string {
	if n <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", n))
}

func padTo(s string, w int, bg lipgloss.Color) string {
	if lipgloss.Width(s) > w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	return s + gap(bg, w-lipgloss.Width(s))
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m *model) View() string {
	if m.w < 60 || m.h < 12 {
		return "\n  LLMux needs at least 60×12 terminal cells.\n"
	}
	if m.showHelp {
		return strings.Join([]string{
			m.header(),
			lipgloss.Place(m.w, m.h-2, lipgloss.Center, lipgloss.Center, m.helpBox()),
			m.statusBar(),
		}, "\n")
	}
	tb, _ := m.toolbar()
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.listPanel(), m.detailPanel())
	return strings.Join([]string{m.header(), tb, body, m.statusBar()}, "\n")
}

// ---------------------------------------------------------------------------
// Header & toolbar
// ---------------------------------------------------------------------------

func (m *model) header() string {
	bg := cBar
	left := gap(bg, 1) + gradient("◆ LLMUX", "#22d3ee", "#a78bfa", bg) +
		on(bg, cDim, false, " v"+m.info.Version) + gap(bg, 2) +
		on(bg, cGreen, true, "●") + on(bg, cMute, false, " "+m.info.Listen) +
		on(bg, cDim, false, fmt.Sprintf("  %d host(s)", len(m.info.Hosts)))

	live := on(bg, cGreen, true, "● LIVE")
	if m.paused {
		live = on(bg, cAmber, true, "⏸ PAUSED")
	}
	s := m.stats
	errCol := cDim
	if s.errors > 0 {
		errCol = cRed
	}
	right := live + gap(bg, 3) +
		on(bg, cAccent, true, "⇅ ") + on(bg, cText, false, fmt.Sprint(s.total)) + on(bg, cDim, false, " req") + gap(bg, 2) +
		on(bg, errCol, true, "✕ ") + on(bg, errCol, false, fmt.Sprint(s.errors)) + gap(bg, 2) +
		on(bg, cViolet, true, "⌀ ") + on(bg, cText, false, fmtDur(s.avg)) + gap(bg, 2) +
		on(bg, cPink, true, "◈ ") + on(bg, cText, false, fmtCount(s.tokens)) + on(bg, cDim, false, " tok") + gap(bg, 1)

	room := m.w - lipgloss.Width(left) - lipgloss.Width(right)
	if room < 1 {
		return padTo(left, m.w, bg)
	}
	return left + gap(bg, room) + right
}

type seg struct {
	s   string
	act string
}

// toolbar renders row 1 and the click targets of its widgets.
func (m *model) toolbar() (string, []hit) {
	for _, compact := range []bool{false, true} {
		line, hits := m.buildToolbar(compact)
		if lipgloss.Width(line) <= m.w || compact {
			return padTo(line, m.w, cBar2), hits
		}
	}
	return "", nil
}

func (m *model) buildToolbar(compact bool) (string, []hit) {
	bg := cBar2
	chip := func(label string, active bool, act string) seg {
		if active {
			return seg{lipgloss.NewStyle().Foreground(cInk).Background(cAccent).Bold(true).Render(" " + label + " "), act}
		}
		return seg{on(bg, cMute, false, " "+label+" "), act}
	}
	button := func(icon, label, key string, active bool, act string) seg {
		txt := " " + icon
		if !compact {
			txt += " " + label
		}
		if active {
			return seg{lipgloss.NewStyle().Foreground(cInk).Background(cViolet).Bold(true).Render(txt + " "), act}
		}
		return seg{on(bg, cText, false, txt) + on(bg, cDim, false, " "+key+" "), act}
	}

	// filter box
	boxW := 28
	if compact {
		boxW = 16
	}
	var box string
	switch {
	case m.focus == paneFilter:
		box = lipgloss.NewStyle().Foreground(cText).Background(cSel).Render(padTo(" ⌕ "+trunc(m.filter, boxW-5), boxW-1, cSel)) +
			lipgloss.NewStyle().Foreground(cAccent).Background(cSel).Render("▏")
	case m.filter != "":
		box = lipgloss.NewStyle().Foreground(cAccent).Background(bg).Render(padTo(" ⌕ "+trunc(m.filter, boxW-4), boxW, bg))
	default:
		box = on(bg, cDim, false, padTo(" ⌕ filter…  /", boxW, bg))
	}

	left := []seg{{box, "filter"}}
	for i := statusFilter(0); i < sfCount; i++ {
		left = append(left, chip(statusFilterNames[i], m.sf == i, fmt.Sprintf("sf:%d", i)))
	}
	dl := "any api"
	if m.dialect != "" {
		dl = m.dialect
	}
	left = append(left, chip("⌬ "+dl, m.dialect != "", "dialect"))

	pause := button("⏸", "Pause", "p", m.paused, "pause")
	if m.paused {
		pause = button("▶", "Resume", "p", true, "pause")
	}
	right := []seg{
		pause,
		button("⌫", "Clear", "c", false, "clear"),
		button("↩", "Wrap", "w", m.wrap, "wrap"),
		button("⇣", "Follow", "f", m.follow, "follow"),
		button("{}", "Raw", "r", m.raw, "raw"),
		button("?", "Help", "?", false, "help"),
	}

	sp := gap(bg, 1)
	var sb strings.Builder
	var hits []hit
	x := 0
	emit := func(g []seg) {
		for _, s := range g {
			w := lipgloss.Width(s.s)
			hits = append(hits, hit{x, x + w, s.act})
			sb.WriteString(s.s)
			x += w
			sb.WriteString(sp)
			x++
		}
	}
	sb.WriteString(sp)
	x++
	emit(left)
	rw := 0
	for _, s := range right {
		rw += lipgloss.Width(s.s) + 1
	}
	if pad := m.w - x - rw; pad > 0 {
		sb.WriteString(gap(bg, pad))
		x += pad
	}
	emit(right)
	return sb.String(), hits
}

// ---------------------------------------------------------------------------
// List panel
// ---------------------------------------------------------------------------

func borderBox(lines []string, focused bool) string {
	c := cBorder
	if focused {
		c = cAccent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c).Render(strings.Join(lines, "\n"))
}

func (m *model) listPanel() string {
	inner := m.leftW() - 2
	rowsH := m.panelH() - 2
	lines := make([]string, 0, rowsH)

	title := bold(cAccent).Render("REQUESTS")
	count := fg(cDim).Render(fmt.Sprintf("%d / %d", len(m.rows), m.stats.total))
	lines = append(lines, " "+title+gapPlain(inner-lipgloss.Width(title)-lipgloss.Width(count)-2)+count+" ")

	if len(m.rows) == 0 {
		lines = append(lines, m.emptyState(inner, rowsH-1)...)
	} else {
		for i := m.offset; i < len(m.rows) && len(lines) < rowsH-1; i++ {
			l1, l2 := m.row(i, inner)
			lines = append(lines, l1, l2)
		}
	}
	for len(lines) < rowsH {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = fit(lines[i], inner)
	}
	return borderBox(lines[:rowsH], m.focus != paneDetail)
}

func gapPlain(n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat(" ", n)
}

func (m *model) emptyState(w, h int) []string {
	center := func(s string) string {
		pad := (w - lipgloss.Width(s)) / 2
		if pad < 0 {
			pad = 0
		}
		return gapPlain(pad) + s
	}
	var out []string
	for i := 0; i < h/2-3; i++ {
		out = append(out, "")
	}
	msg := "Waiting for traffic"
	if len(m.rows) == 0 && m.stats.total > 0 {
		msg = "No requests match the current filters"
	}
	out = append(out,
		center(fg(cAccent).Render(spinner[m.frame%len(spinner)])+" "+bold(cText).Render(msg)),
		"",
		center(fg(cDim).Render("point a client at")),
		center(fg(cViolet).Render("http://"+m.info.Listen+"/v1")),
		center(fg(cViolet).Render("http://"+m.info.Listen+"/api")),
	)
	return out
}

func (m *model) row(i, w int) (string, string) {
	s := m.rows[i]
	sel := i == m.cursor
	st := func(c lipgloss.Color, b bool) lipgloss.Style {
		x := lipgloss.NewStyle().Foreground(c).Bold(b)
		if sel {
			x = x.Background(cSel)
		}
		return x
	}
	bgc := lipgloss.Color("")
	if sel {
		bgc = cSel
	}
	sp := st(cText, false).Render(" ")

	bar := " "
	if sel {
		bar = st(cAccent, true).Render("▌")
	}
	var code string
	col := statusColor(s.Status, s.Done)
	switch {
	case !s.Done && s.Status == 0:
		code = spinner[m.frame%len(spinner)] + "  "
	case s.Status == 0:
		code = "ERR"
	default:
		code = fmt.Sprintf("%d", s.Status)
	}
	path := trunc(s.Path, w-17)
	l1 := bar + st(col, true).Render("●") + sp + st(col, true).Render(fmt.Sprintf("%-3s", code)) + sp +
		st(methodColor(s.Method), true).Render(fmt.Sprintf("%-6s", s.Method)) + st(cText, sel).Render(path)

	glyph := "→"
	gcol := cAccent
	if s.Mode == "translate" {
		glyph, gcol = "⇄", cViolet
	}
	model := s.Model
	if model == "" {
		model = "—"
	}
	leftS := st(cDim, false).Render("   "+s.Started.Format("15:04:05")+" ") +
		st(gcol, false).Render(glyph) + st(cMute, false).Render(" "+s.Dialect+" · ") +
		st(cText, false).Render(model)
	var rightS string
	if s.Prompt+s.Completion > 0 {
		rightS += st(cBlue, false).Render("↑"+fmtCount(s.Prompt)) + sp + st(cGreen, false).Render("↓"+fmtCount(s.Completion)) + sp
	}
	if s.Stream {
		rightS += st(cPink, false).Render("≈ ")
	}
	rightS += st(cDim, false).Render(fmtDur(s.Duration)) + sp
	room := w - lipgloss.Width(rightS)
	if room < 8 {
		room = 8
	}
	l2 := padTo(leftS, room, bgc) + rightS
	return padTo(l1, w, bgc), padTo(l2, w, bgc)
}

// ---------------------------------------------------------------------------
// Detail panel
// ---------------------------------------------------------------------------

func (m *model) tabStrip() (string, []hit) {
	compact := m.detailW() < 64
	var sb strings.Builder
	var hits []hit
	x := m.leftW() + 1
	for i := tabID(0); i < tabCount; i++ {
		name := tabNames[i][0]
		if compact {
			name = tabNames[i][1]
		}
		label := fmt.Sprintf(" %d %s ", i+1, name)
		w := lipgloss.Width(label)
		if i == m.tab {
			sb.WriteString(lipgloss.NewStyle().Foreground(cInk).Background(cAccent).Bold(true).Render(label))
		} else {
			sb.WriteString(fg(cMute).Render(label))
		}
		hits = append(hits, hit{x, x + w, ""})
		x += w
		sb.WriteString(" ")
		x++
	}
	return sb.String(), hits
}

func (m *model) tabHits() []hit {
	_, h := m.tabStrip()
	return h
}

func (m *model) detailPanel() string {
	inner := m.detailW()
	h := m.detailH()
	tabs, _ := m.tabStrip()
	lines := []string{fit(" "+tabs, inner)}

	if len(m.detail) == 0 {
		hint := fg(cDim).Render("  Select a request to inspect it.")
		lines = append(lines, fit("", inner), fit(hint, inner))
	} else {
		total := len(m.detail)
		thumbLen, thumbPos := h, 0
		if total > h {
			thumbLen = h * h / total
			if thumbLen < 1 {
				thumbLen = 1
			}
			thumbPos = m.dOff * (h - thumbLen) / (total - h)
		}
		for i := 0; i < h; i++ {
			text := ""
			if idx := m.dOff + i; idx < total {
				text = m.detail[idx]
			}
			bar := fg(cBorder).Render("│")
			if total > h && i >= thumbPos && i < thumbPos+thumbLen {
				bar = fg(cAccent).Render("┃")
			} else if total <= h {
				bar = " "
			}
			lines = append(lines, fit(text, inner-1)+bar)
		}
	}
	for len(lines) < h+1 {
		lines = append(lines, fit("", inner))
	}
	return borderBox(lines[:h+1], m.focus == paneDetail)
}

// ---------------------------------------------------------------------------
// Status bar & help
// ---------------------------------------------------------------------------

func (m *model) statusBar() string {
	bg := cBar
	key := func(k, d string) string {
		return on(bg, cAccent, true, k) + on(bg, cMute, false, " "+d+"  ")
	}
	var hints string
	switch {
	case m.showHelp:
		hints = key("esc", "close")
	case m.focus == paneFilter:
		hints = key("type", "to filter") + key("⏎/esc", "done") + key("ctrl+u", "clear")
	case m.focus == paneDetail:
		hints = key("↑↓", "scroll") + key("space/b", "page") + key("←→", "tab") + key("⇥", "list") + key("w", "wrap") + key("r", "raw") + key("?", "help") + key("q", "quit")
	default:
		hints = key("↑↓", "move") + key("⏎/⇥", "details") + key("/", "filter") + key("s", "status") + key("d", "api") + key("p", "pause") + key("c", "clear") + key("?", "help") + key("q", "quit")
	}

	pos := "0/0"
	if len(m.rows) > 0 {
		pos = fmt.Sprintf("%d/%d", m.cursor+1, len(m.rows))
	}
	pct := 100
	if max := len(m.detail) - m.detailH(); max > 0 {
		pct = m.dOff * 100 / max
	}
	right := on(bg, cDim, false, "item ") + on(bg, cText, false, pos) +
		on(bg, cDim, false, "  scroll ") + on(bg, cText, false, fmt.Sprintf("%d%%", pct)) + gap(bg, 1)
	room := m.w - lipgloss.Width(right)
	return padTo(gap(bg, 1)+hints, room, bg) + right
}

func (m *model) helpBox() string {
	row := func(k, d string) string {
		return bold(cAccent).Render(fmt.Sprintf("%-16s", k)) + fg(cText).Render(d)
	}
	title := gradient("◆ LLMUX · keyboard & mouse", "#22d3ee", "#a78bfa", "")
	lines := []string{
		title, "",
		fg(cViolet).Render("Navigate"),
		row("↑ ↓  j k", "move selection / scroll details"),
		row("PgUp PgDn", "page"),
		row("g  G", "first / last"),
		row("Tab  ⇧Tab", "switch list ⇄ details"),
		row("← →  h l  [ ]", "previous / next detail tab"),
		row("1 … 5", "jump to tab"),
		row("J  K", "scroll details from the list"),
		"",
		fg(cViolet).Render("Filter"),
		row("/", "text filter (all words must match)"),
		row("s", "status: ALL → 2xx → ERR → STREAM"),
		row("d", "cycle API dialect"),
		row("Esc", "clear filters"),
		"",
		fg(cViolet).Render("Actions"),
		row("p", "pause / resume live updates"),
		row("f", "follow newest request"),
		row("w", "wrap long lines"),
		row("r", "raw bodies (no JSON formatting)"),
		row("c", "clear history"),
		row("q  Ctrl+C", "quit (stops the server)"),
		"",
		fg(cDim).Render("Mouse: click rows, tabs and toolbar buttons · wheel scrolls"),
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cViolet).Padding(1, 3).
		Render(strings.Join(lines, "\n"))
}
