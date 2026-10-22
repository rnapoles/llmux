package inspector

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Store keeps the most recent exchanges in a bounded ring.
type Store struct {
	mu      sync.RWMutex
	items   []*Exchange
	cap     int
	limit   int
	nextID  atomic.Uint64
	version atomic.Uint64
}

// NewStore creates a store keeping at most capacity exchanges and bodyLimit
// bytes per captured body.
func NewStore(capacity, bodyLimit int) *Store {
	if capacity <= 0 {
		capacity = 1000
	}
	if bodyLimit <= 0 {
		bodyLimit = 1 << 20
	}
	return &Store{cap: capacity, limit: bodyLimit}
}

// Begin registers a new in-flight exchange. A nil Store returns nil, which is
// a valid no-op recorder.
func (s *Store) Begin() *Exchange {
	if s == nil {
		return nil
	}
	e := &Exchange{limit: s.limit, store: s}
	e.rec.ID = s.nextID.Add(1)
	e.rec.Started = time.Now()
	s.mu.Lock()
	s.items = append(s.items, e)
	if len(s.items) > s.cap {
		s.items = append([]*Exchange(nil), s.items[len(s.items)-s.cap:]...)
	}
	s.mu.Unlock()
	s.touch()
	return e
}

// List returns the exchanges oldest→newest.
func (s *Store) List() []*Exchange {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*Exchange(nil), s.items...)
}

// Clear drops all recorded exchanges.
func (s *Store) Clear() {
	s.mu.Lock()
	s.items = nil
	s.mu.Unlock()
	s.touch()
}

// Version changes whenever anything is recorded; UIs poll it cheaply.
func (s *Store) Version() uint64 { return s.version.Load() }

func (s *Store) touch() {
	if s != nil {
		s.version.Add(1)
	}
}

type ctxKey struct{}

// WithExchange attaches e to ctx so lower layers (the upstream transport) can
// record into it.
func WithExchange(ctx context.Context, e *Exchange) context.Context {
	return context.WithValue(ctx, ctxKey{}, e)
}

// FromContext returns the exchange attached to ctx, or nil.
func FromContext(ctx context.Context) *Exchange {
	e, _ := ctx.Value(ctxKey{}).(*Exchange)
	return e
}
