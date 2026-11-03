package tui

import "github.com/mattn/go-runewidth"

// headCells returns the longest prefix of s that fits in w terminal cells,
// ending in "…" when something was cut. Width is measured in cells, not runes,
// so CJK and other wide text cannot overflow the space it was given.
func headCells(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w, "…")
}

// tailCells returns the longest suffix of s that fits in w cells, prefixed with
// "…" when the start was cut. It is used for text being typed, where the end
// (next to the cursor) is what matters.
func tailCells(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	rs := []rune(s)
	width, i := 1, len(rs) // 1 cell reserved for the leading "…"
	for i > 0 {
		rw := runewidth.RuneWidth(rs[i-1])
		if width+rw > w {
			break
		}
		width += rw
		i--
	}
	return "…" + string(rs[i:])
}
