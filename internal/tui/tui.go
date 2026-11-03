// Package tui implements LLMux's interactive traffic inspector.
//
// It is built on Bubble Tea v2, whose renderer draws into a fixed grid of
// terminal cells: content that is too wide or too tall is clipped instead of
// wrapping onto extra rows, so no payload can make the frame grow and scroll
// the header off screen. The detail pane scrolls with the Bubbles viewport.
package tui

import (
	tea "charm.land/bubbletea/v2"

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
	m := newModel(store, info)
	p := tea.NewProgram(m) // alt screen and mouse mode are declared by View()
	go func() {
		if err, ok := <-errc; ok && err != nil {
			p.Send(serverErrMsg{err})
		}
	}()
	if _, err := p.Run(); err != nil {
		return err
	}
	return m.fatal
}
