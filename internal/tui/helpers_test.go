package tui

import (
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// kp builds the Bubble Tea v2 key event for a key name such as "j", "J", "?",
// "tab", "shift+tab", "esc", "pgdown", "space" or "ctrl+u".
func kp(s string) tea.KeyPressMsg {
	switch s {
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	special := map[string]rune{
		"tab": tea.KeyTab, "esc": tea.KeyEscape, "enter": tea.KeyEnter, "backspace": tea.KeyBackspace,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
	}
	if c, ok := special[s]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	r := []rune(s)[0]
	k := tea.KeyPressMsg{Code: unicode.ToLower(r), Text: s}
	if unicode.IsUpper(r) {
		k.Mod = tea.ModShift
	}
	return k
}

func click(x, y int) tea.MouseClickMsg { return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft} }

func wheel(x, y int, up bool) tea.MouseWheelMsg {
	b := tea.MouseWheelDown
	if up {
		b = tea.MouseWheelUp
	}
	return tea.MouseWheelMsg{X: x, Y: y, Button: b}
}

func paste(text string) tea.PasteMsg { return tea.PasteMsg{Content: text} }
