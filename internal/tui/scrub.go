package tui

import "strings"

// scrub is the last filter before bytes reach the terminal. Frames are built
// from text that has already been cleaned, so this should never change
// anything; it exists so that no bug elsewhere can let content inject escape
// sequences or stray control characters. Only SGR colour sequences
// (ESC [ ... m) are allowed through: they are the only escapes this package
// produces (cursor placement belongs to the Bubble Tea renderer).
func scrub(s string) string {
	// Fast path: nothing but printable ASCII, UTF-8 text and our own sequences.
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			clean = false
			break
		}
		if s[i] == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f { // C1 control
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			// allow ESC [ <digits;> m only
			j := i + 1
			if j < len(s) && s[j] == '[' {
				k := j + 1
				for k < len(s) && (s[k] >= '0' && s[k] <= '9' || s[k] == ';') {
					k++
				}
				if k < len(s) && s[k] == 'm' {
					sb.WriteString(s[i : k+1])
					i = k + 1
					continue
				}
			}
			i++ // drop the ESC; what follows is harmless text
		case c < 0x20 || c == 0x7f:
			i++
		case c == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f:
			i += 2
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return sb.String()
}
