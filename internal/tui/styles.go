package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Palette: deep-space neutrals with neon cyan/violet accents.
var (
	cBar    = lipgloss.Color("#0f172a")
	cBar2   = lipgloss.Color("#162038")
	cSel    = lipgloss.Color("#1b2a4a")
	cAccent = lipgloss.Color("#22d3ee")
	cViolet = lipgloss.Color("#a78bfa")
	cPink   = lipgloss.Color("#f472b6")
	cGreen  = lipgloss.Color("#4ade80")
	cAmber  = lipgloss.Color("#fbbf24")
	cRed    = lipgloss.Color("#f87171")
	cBlue   = lipgloss.Color("#60a5fa")
	cText   = lipgloss.Color("#e2e8f0")
	cMute   = lipgloss.Color("#94a3b8")
	cDim    = lipgloss.Color("#64748b")
	cBorder = lipgloss.Color("#334155")
	cInk    = lipgloss.Color("#06121f") // dark text on bright fills
)

func fg(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

func bold(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c).Bold(true) }

// gradient colours each rune of s between two #rrggbb colours.
func gradient(s, from, to string, bg lipgloss.Color) string {
	r1, g1, b1 := hexRGB(from)
	r2, g2, b2 := hexRGB(to)
	runes := []rune(s)
	var sb strings.Builder
	for i, r := range runes {
		t := 0.0
		if len(runes) > 1 {
			t = float64(i) / float64(len(runes)-1)
		}
		c := fmt.Sprintf("#%02x%02x%02x",
			int(float64(r1)+(float64(r2)-float64(r1))*t),
			int(float64(g1)+(float64(g2)-float64(g1))*t),
			int(float64(b1)+(float64(b2)-float64(b1))*t))
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
		if bg != "" {
			st = st.Background(bg)
		}
		sb.WriteString(st.Render(string(r)))
	}
	return sb.String()
}

func hexRGB(h string) (int, int, int) {
	var r, g, b int
	fmt.Sscanf(strings.TrimPrefix(h, "#"), "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func statusColor(code int, done bool) lipgloss.Color {
	switch {
	case !done && code == 0:
		return cAccent
	case code >= 500 || code == 0:
		return cRed
	case code >= 400:
		return cAmber
	case code >= 300:
		return cBlue
	default:
		return cGreen
	}
}

func methodColor(m string) lipgloss.Color {
	switch m {
	case "GET":
		return cBlue
	case "POST":
		return cGreen
	case "PUT", "PATCH":
		return cAmber
	case "DELETE":
		return cRed
	}
	return cViolet
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func fmtBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

func fmtCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// fit truncates or pads s (which may contain ANSI) to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// trunc shortens plain text with an ellipsis.
func trunc(s string, w int) string {
	r := []rune(s)
	if w <= 0 {
		return ""
	}
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
