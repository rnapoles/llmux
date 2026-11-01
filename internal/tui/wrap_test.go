package tui

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

var sgrRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripSGR(s string) string { return sgrRe.ReplaceAllString(s, "") }

// refClean is the obviously-correct (slow) specification of clean().
func refClean(s string, keepNL bool) string {
	rs := []rune(s)
	var sb strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\n':
			if keepNL {
				sb.WriteByte('\n')
			} else {
				sb.WriteByte(' ')
			}
		case r == '\t':
			sb.WriteByte(' ')
		case isRegionalIndicator(r):
			sb.WriteByte('[')
			sb.WriteRune('A' + (r - 0x1f1e6))
			if i+1 < len(rs) && isRegionalIndicator(rs[i+1]) {
				sb.WriteRune('A' + (rs[i+1] - 0x1f1e6))
				i++
			}
			sb.WriteByte(']')
		case unsafeRune(r):
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func randText(rng *rand.Rand, n int) string {
	alphabet := []string{"a", "b", "word", "longerword", " ", " ", "  ", "日", "本", "😀", "é", "\n", "\t", "\x1b",
		"\u200d", "\ufe0f", "🇺🇸", "\u202e", "x", "1", ",", "{", "\"", "\r", "\u0301"}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(alphabet[rng.Intn(len(alphabet))])
	}
	return sb.String()
}

func TestCleanMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		s := randText(rng, rng.Intn(40))
		for _, nl := range []bool{true, false} {
			if got, want := clean(s, nl), refClean(s, nl); got != want {
				t.Fatalf("clean(%q, %v) = %q, want %q", s, nl, got, want)
			}
		}
	}
	if !utf8.ValidString(clean("\xff\xfe broken", true)) {
		t.Error("invalid UTF-8 input must not produce invalid output")
	}
}

func TestWrapTextProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	colors := []string{sgrKey, sgrString, sgrNumber, sgrKeyword, sgrPunct}
	for iter := 0; iter < 3000; iter++ {
		w := 3 + rng.Intn(40)
		// build coloured text: random tokens, some wrapped in colour codes
		var sb strings.Builder
		for n := rng.Intn(30); n > 0; n-- {
			tok := clean(randText(rng, 1+rng.Intn(6)), true)
			if rng.Intn(3) == 0 {
				sb.WriteString(colors[rng.Intn(len(colors))] + tok + sgrReset)
			} else {
				sb.WriteString(tok)
			}
		}
		in := sb.String()
		lines := wrapText(in, w, true)

		var plainOut strings.Builder
		for _, l := range lines {
			vis := stripSGR(l)
			// a single wide rune can exceed w=... only when w is 1; we use w >= 3
			if cw := runewidth.StringWidth(vis); cw > w {
				t.Fatalf("w=%d line is %d cells: %q (input %q)", w, cw, vis, in)
			}
			// colour must never leak past the end of a line
			opens := 0
			for _, m := range sgrRe.FindAllString(l, -1) {
				if m == sgrReset {
					opens = 0
				} else {
					opens++
				}
			}
			if opens > 0 {
				t.Fatalf("line leaves a colour open: %q\n lines=%q\n in=%q w=%d", l, lines, in, w)
			}
			plainOut.WriteString(vis)
		}
		// no visible character may be lost or reordered (only spaces and newlines may vanish)
		squash := func(s string) string {
			return strings.NewReplacer(" ", "", "\n", "").Replace(s)
		}
		if got, want := squash(plainOut.String()), squash(stripSGR(in)); got != want {
			t.Fatalf("text changed by wrapping\n got: %q\nwant: %q", got, want)
		}
	}
}

func TestWrapTextBehaviour(t *testing.T) {
	got := wrapText("alpha beta gamma delta", 11, true)
	if strings.Join(got, "|") != "alpha beta|gamma delta" {
		t.Errorf("should break at spaces: %q", got)
	}
	got = wrapText("abcdefghij", 4, true)
	if strings.Join(got, "|") != "abcd|efgh|ij" {
		t.Errorf("should hard-break long words: %q", got)
	}
	got = wrapText("    indented text here", 10, true)
	if len(got) < 2 || !strings.HasPrefix(got[0], "    ") || strings.TrimSpace(got[0]) == "" {
		t.Errorf("indentation must not become an empty first line: %q", got)
	}
	// colour carried across a break inside one coloured token
	got = wrapText(sgrString+"abcdefgh"+sgrReset, 4, true)
	if len(got) != 2 || !strings.HasPrefix(got[1], sgrString) || !strings.HasSuffix(got[0], sgrReset) {
		t.Errorf("colour not carried: %q", got)
	}
	if got := wrapText("a\n\nb", 10, true); len(got) != 3 || got[1] != "" {
		t.Errorf("blank lines must survive: %q", got)
	}
	if got := wrapText("no wrap here at all", 5, false); len(got) != 1 {
		t.Errorf("wrap=false must not wrap: %q", got)
	}
}
