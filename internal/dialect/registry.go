// Package dialect wires the available client-facing API emulations.
package dialect

import (
	"fmt"
	"sort"

	"github.com/rnapoles/llmux/internal/core"
	ollamad "github.com/rnapoles/llmux/internal/dialect/ollama"
	openaid "github.com/rnapoles/llmux/internal/dialect/openai"
)

// Registry maps dialect names to implementations.
type Registry struct{ m map[string]core.Dialect }

func NewRegistry() *Registry { return &Registry{m: map[string]core.Dialect{}} }

func (r *Registry) Register(d core.Dialect) { r.m[d.Name()] = d }

func (r *Registry) Get(name string) (core.Dialect, error) {
	d, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q (available: %v)", name, r.Names())
	}
	return d, nil
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Default returns a registry with every built-in dialect.
// To add a new API (e.g. Anthropic) implement core.Dialect and register it here.
func Default() *Registry {
	r := NewRegistry()
	r.Register(openaid.New())
	r.Register(ollamad.New())
	return r
}
