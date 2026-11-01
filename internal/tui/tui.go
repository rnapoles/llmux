// Package tui implements LLMux's interactive traffic inspector.
package tui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rnapoles/llmux/internal/inspector"
)

// HostInfo describes a virtual host for the header/help screens.
type HostInfo struct {
	Name     string
	Patterns []string
	Dialects []string
	Upstream string
	Type     string
}

// Info is static data about the running gateway.
type Info struct {
	Version string
	Listen  string
	Hosts   []HostInfo
	Persist string // SQLite path when persistence is on
	Redact  bool   // mask credentials in displayed headers
}

// Run shows the TUI until the user quits or the server fails (errc).
func Run(store *inspector.Store, info Info, errc <-chan error) error {
	// The palette is designed for dark terminals; pin it so no adaptive colour
	// logic can ever flip it.
	lipgloss.SetHasDarkBackground(true)
	m := newModel(store, info)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	go func() {
		if err, ok := <-errc; ok && err != nil {
			p.Send(serverErrMsg{err})
		}
	}()
	// DECAWM off: if a terminal ever disagrees with us about a character's
	// width, the overflow is clipped instead of wrapping onto a new row, which
	// would push the whole frame down and scroll the header off screen.
	fmt.Fprint(os.Stdout, "\x1b[?7l")
	defer fmt.Fprint(os.Stdout, "\x1b[?7h")
	_, err := p.Run()
	if err != nil {
		return err
	}
	return m.fatal
}
