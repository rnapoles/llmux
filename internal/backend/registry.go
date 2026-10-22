// Package backend wires the available upstream provider implementations.
package backend

import (
	"fmt"
	"sort"

	"github.com/rnapoles/llmux/internal/backend/ollama"
	"github.com/rnapoles/llmux/internal/backend/openai"
	"github.com/rnapoles/llmux/internal/core"
)

// Registry maps provider type names to factories.
type Registry struct {
	m map[string]core.BackendFactory
}

func NewRegistry() *Registry { return &Registry{m: map[string]core.BackendFactory{}} }

func (r *Registry) Register(name string, f core.BackendFactory) { r.m[name] = f }

func (r *Registry) Build(name string, spec core.BackendSpec) (core.Backend, error) {
	f, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("unknown upstream type %q (available: %v)", name, r.Names())
	}
	return f(spec)
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Default returns a registry with every built-in backend.
// To add a new provider (e.g. Anthropic) implement core.Backend and register
// its factory here.
func Default() *Registry {
	r := NewRegistry()
	r.Register(openai.Name, openai.Factory)
	r.Register(ollama.Name, ollama.Factory)
	return r
}
