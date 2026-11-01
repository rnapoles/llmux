package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// wrapText turns coloured text into display lines of at most w cells.
//
// It is a deliberately small replacement for lipgloss' general-purpose
// wrapper, which measures every grapheme cluster and was ~75% of the time
// spent building a detail view. It only has to understand the SGR colour
// sequences this package itself emits. The active colour is carried across
// every line break (wrapped or not): a line that ends inside a coloured token
// is closed with a reset and the next line re-opens the colour, so colour can
// never bleed from one terminal row into the next.
//
// With wrap=false, lines are only split at "\n" (the renderer truncates each
// visible line when drawing it).
func wrapText(s string, w int, wrap bool) []string {
	if w < 1 {
		w = 1
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	carry := "" // colour in effect at the start of the next line
	var wr wrapper
	for _, ln := range lines {
		plain := carry == "" && strings.IndexByte(ln, 0x1b) < 0
		switch {
		case plain && (!wrap || len(ln) <= w): // len(ln) >= cells: certainly fits
			out = append(out, ln)
		case !wrap:
			ln, carry = closeColour(carry, ln)
			out = append(out, ln)
		default:
			out, carry = wr.line(out, ln, w, carry)
		}
	}
	return out
}

// closeColour re-opens the inherited colour at the start of ln and closes any
// colour still open at its end, returning the colour to carry onwards.
func closeColour(carry, ln string) (string, string) {
	active := carry
	for i := 0; i < len(ln); i++ {
		if ln[i] != 0x1b {
			continue
		}
		seq, n := sgrAt(ln, i)
		if n == 0 {
			continue
		}
		if seq == sgrReset {
			active = ""
		} else {
			active = seq
		}
		i += n - 1
	}
	if carry != "" {
		ln = carry + ln
	}
	if active != "" {
		ln += sgrReset
	}
	return ln, active
}

// sgrAt parses an ESC[...m style sequence at s[i]; n==0 if there is none.
func sgrAt(s string, i int) (seq string, n int) {
	if i+1 >= len(s) || s[i] != 0x1b || s[i+1] != '[' {
		return "", 0
	}
	k := i + 2
	for k < len(s) && (s[k] < 0x40 || s[k] > 0x7e) {
		k++
	}
	if k >= len(s) {
		return "", 0
	}
	k++
	return s[i:k], k - i
}

// wrapper holds reusable buffers so wrapping a huge single line allocates per
// output line, not per input byte.
type wrapper struct {
	buf, tmp []byte
}

func (wr *wrapper) line(out []string, ln string, w int, carry string) ([]string, string) {
	buf := append(wr.buf[:0], carry...)
	var (
		curW        int
		active      = carry
		spacePos    = -1 // index in buf of the last space we may break at
		spaceW      int  // cell width of buf[:spacePos]
		spaceActive string
		seenText    bool // a non-space rune precedes on this line (never break indentation)
	)
	emit := func(b []byte, open string) {
		if open != "" {
			b = append(b, sgrReset...)
		}
		out = append(out, string(b))
	}
	for i := 0; i < len(ln); {
		if ln[i] == 0x1b {
			if seq, n := sgrAt(ln, i); n > 0 {
				buf = append(buf, seq...)
				if seq == sgrReset {
					active = ""
				} else {
					active = seq
				}
				i += n
				continue
			}
		}
		var (
			r    rune
			size = 1
			rw   = 1
		)
		if c := ln[i]; c < utf8.RuneSelf { // printable ASCII: one byte, one cell
			r = rune(c)
		} else {
			r, size = utf8.DecodeRuneInString(ln[i:])
			rw = runewidth.RuneWidth(r)
		}

		for curW+rw > w && curW > 0 {
			if spacePos >= 0 {
				wr.tmp = append(append(wr.tmp[:0], spaceActive...), buf[spacePos+1:]...)
				restW := curW - spaceW - 1
				emit(buf[:spacePos], spaceActive)
				buf = append(buf[:0], wr.tmp...)
				curW = restW
			} else {
				emit(buf, active)
				buf = append(buf[:0], active...)
				curW = 0
			}
			spacePos, seenText = -1, curW > 0
		}
		buf = append(buf, ln[i:i+size]...)
		curW += rw
		if r == ' ' {
			if seenText {
				spacePos, spaceW, spaceActive = len(buf)-1, curW-1, active
			}
		} else {
			seenText = true
		}
		i += size
	}
	if active != "" {
		buf = append(buf, sgrReset...)
	}
	out = append(out, string(buf))
	wr.buf = buf
	return out, active
}
