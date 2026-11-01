package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Highlighting uses raw 256-colour SGR codes: they are fast to emit for
// megabyte payloads and work on virtually every terminal.
const (
	sgrReset   = "\x1b[0m"
	sgrKey     = "\x1b[38;5;81m"
	sgrString  = "\x1b[38;5;114m"
	sgrNumber  = "\x1b[38;5;215m"
	sgrKeyword = "\x1b[38;5;141m"
	sgrPunct   = "\x1b[38;5;244m"
	sgrDim     = "\x1b[38;5;242m"
)

// unsafeRune reports runes that are removed from anything shown on screen:
// control characters (notably ESC, which could drive the terminal), zero-width
// and joiner characters, variation selectors, bidi controls and line/paragraph
// separators. Terminals and Go's width tables disagree about how wide these
// are, and a single disagreement makes the terminal wrap a row and tear the
// whole layout apart.
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		return true
	case r == 0x00ad || r == 0x034f || r == 0x061c || r == 0x180e:
		return true
	case r >= 0x200b && r <= 0x200f, // zero-width space, ZWNJ, ZWJ, LRM, RLM
		r == 0x2028 || r == 0x2029,
		r >= 0x202a && r <= 0x202e, // bidi embeddings / overrides
		r >= 0x2060 && r <= 0x206f, // word joiner, invisible operators, bidi isolates
		r >= 0xfe00 && r <= 0xfe0f, // variation selectors (VS16 emoji presentation)
		r == 0xfeff,
		r >= 0xfff9 && r <= 0xfffb,
		r >= 0xe0000 && r <= 0xe007f, // tag characters
		r >= 0xe0100 && r <= 0xe01ef:
		return true
	}
	return false
}

func isRegionalIndicator(r rune) bool { return r >= 0x1f1e6 && r <= 0x1f1ff }

// clean is the single text filter for display. Newlines survive only when
// keepNL is set; flag emoji (pairs of regional indicators, whose width varies
// between terminals) become plain "[US]"-style text.
func clean(s string, keepNL bool) string {
	// Fast path: printable ASCII (plus newline when kept) needs no work at all.
	i := 0
	for ; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f {
			if !(c == '\n' && keepNL) {
				break
			}
		}
	}
	if i == len(s) {
		return s
	}

	var sb strings.Builder
	sb.Grow(len(s))
	sb.WriteString(s[:i])
	for i < len(s) {
		c := s[i]
		if c < utf8.RuneSelf { // ASCII
			switch {
			case c == '\n':
				if keepNL {
					sb.WriteByte('\n')
				} else {
					sb.WriteByte(' ')
				}
			case c == '\t':
				sb.WriteByte(' ')
			case c < 0x20 || c == 0x7f:
				// dropped
			default:
				sb.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case isRegionalIndicator(r):
			sb.WriteByte('[')
			sb.WriteRune('A' + (r - 0x1f1e6))
			i += size
			if i < len(s) {
				if r2, sz2 := utf8.DecodeRuneInString(s[i:]); isRegionalIndicator(r2) {
					sb.WriteRune('A' + (r2 - 0x1f1e6))
					i += sz2
				}
			}
			sb.WriteByte(']')
			continue
		case unsafeRune(r):
			// dropped
		case r == utf8.RuneError && size == 1:
			sb.WriteRune(utf8.RuneError) // invalid byte -> U+FFFD, never emitted raw
		default:
			sb.WriteString(s[i : i+size])
		}
		i += size
	}
	return sb.String()
}

// sanitize cleans multi-line text (bodies); inline cleans single-line fields.
func sanitize(s string) string { return clean(s, true) }
func inline(s string) string   { return clean(s, false) }

// colorizeJSON highlights JSON (or JSON-ish) text. It is tolerant: invalid or
// truncated input is highlighted as far as it parses.
func colorizeJSON(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) * 2)
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			end := j + 1
			if end > len(s) {
				end = len(s)
			}
			k := end
			for k < len(s) && s[k] == ' ' {
				k++
			}
			col := sgrString
			if k < len(s) && s[k] == ':' {
				col = sgrKey
			}
			sb.WriteString(col)
			sb.WriteString(s[i:end])
			sb.WriteString(sgrReset)
			i = end
		case c == '-' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(s) && strings.IndexByte("0123456789+-.eE", s[j]) >= 0 {
				j++
			}
			sb.WriteString(sgrNumber + s[i:j] + sgrReset)
			i = j
		case c >= 'a' && c <= 'z':
			j := i
			for j < len(s) && s[j] >= 'a' && s[j] <= 'z' {
				j++
			}
			w := s[i:j]
			if w == "true" || w == "false" || w == "null" {
				sb.WriteString(sgrKeyword + w + sgrReset)
			} else {
				sb.WriteString(w)
			}
			i = j
		case strings.IndexByte("{}[],:", c) >= 0:
			sb.WriteString(sgrPunct)
			sb.WriteByte(c)
			sb.WriteString(sgrReset)
			i++
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return sb.String()
}

// prettyJSON indents valid JSON; returns ok=false otherwise.
func prettyJSON(b []byte) (string, bool) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || (b[0] != '{' && b[0] != '[') || !json.Valid(b) {
		return "", false
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return "", false
	}
	return out.String(), true
}

func compactJSON(b []byte) (string, bool) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' || !json.Valid(b) {
		return "", false
	}
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return "", false
	}
	return out.String(), true
}
