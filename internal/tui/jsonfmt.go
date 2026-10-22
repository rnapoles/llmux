package tui

import (
	"bytes"
	"encoding/json"
	"strings"
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

// sanitize removes control characters (notably ESC) so untrusted payloads
// cannot manipulate the terminal.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

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
