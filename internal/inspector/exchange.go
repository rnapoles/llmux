// Package inspector records proxied traffic so it can be browsed live (TUI).
// All types are safe for concurrent use; a nil *Exchange is a valid no-op
// recorder, which lets the gateway run without any inspector overhead.
package inspector

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const tailSize = 4096

// Body is a capped capture of a payload. Data keeps the head; Tail keeps the
// last bytes so end-of-stream usage statistics survive truncation.
type Body struct {
	Data      []byte
	Tail      []byte
	Total     int64
	Truncated bool
}

func (b *Body) append(p []byte, limit int) {
	b.Total += int64(len(p))
	if room := limit - len(b.Data); room > 0 {
		n := len(p)
		if n > room {
			n = room
		}
		b.Data = append(b.Data, p[:n]...)
	}
	if b.Total > int64(limit) {
		b.Truncated = true
		b.Tail = append(b.Tail, p...)
		if len(b.Tail) > tailSize {
			b.Tail = append([]byte(nil), b.Tail[len(b.Tail)-tailSize:]...)
		}
	}
}

func (b Body) cloneLimit(max int) Body {
	data := b.Data
	if max >= 0 && len(data) > max {
		data = data[:max]
	}
	return Body{
		Data:      append([]byte(nil), data...),
		Tail:      append([]byte(nil), b.Tail...),
		Total:     b.Total,
		Truncated: b.Truncated,
	}
}

// Leg is one HTTP conversation: client↔LLMux or LLMux↔upstream.
type Leg struct {
	Method     string
	URL        string
	Status     int
	ReqHeader  http.Header
	RespHeader http.Header
	ReqBody    Body
	RespBody   Body
}

// Record is the full, plain-data description of an exchange.
type Record struct {
	ID         uint64
	Started    time.Time
	Finished   time.Time
	FirstByte  time.Time
	Done       bool
	ClientAddr string
	Host       string
	VHost      string
	Dialect    string
	Backend    string
	Operation  string
	Mode       string // "proxy" (transparent) | "translate"
	Model      string
	Stream     bool
	Prompt     int
	Completion int
	Client     Leg
	Upstream   Leg
	Error      string
}

// Duration is the elapsed (or running) time.
func (r *Record) Duration() time.Duration {
	if r.Done {
		return r.Finished.Sub(r.Started)
	}
	return time.Since(r.Started)
}

// Exchange is a live, mutable Record.
type Exchange struct {
	mu    sync.RWMutex
	rec   Record
	limit int
	store *Store
	rev   atomic.Uint64
}

// Rev increases on every mutation; use it as a cheap change detector.
func (e *Exchange) Rev() uint64 { return e.rev.Load() }

// Update mutates the record under lock and notifies the store.
func (e *Exchange) Update(fn func(r *Record)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	fn(&e.rec)
	e.mu.Unlock()
	e.rev.Add(1)
	e.store.touch()
}

// Limit is the per-body capture limit.
func (e *Exchange) Limit() int {
	if e == nil {
		return 0
	}
	return e.limit
}

// AppendBody appends captured bytes to the selected body.
func (e *Exchange) AppendBody(sel func(r *Record) *Body, p []byte) {
	if e == nil || len(p) == 0 {
		return
	}
	e.mu.Lock()
	sel(&e.rec).append(p, e.limit)
	e.mu.Unlock()
	e.rev.Add(1)
	e.store.touch()
}

// Trim shrinks every captured body to at most limit bytes (keeping the tail
// for usage statistics). It is used after a record has been persisted so the
// in-memory history does not retain very large payloads.
func (e *Exchange) Trim(limit int) {
	if e == nil || limit <= 0 {
		return
	}
	e.mu.Lock()
	for _, b := range []*Body{&e.rec.Client.ReqBody, &e.rec.Client.RespBody, &e.rec.Upstream.ReqBody, &e.rec.Upstream.RespBody} {
		if len(b.Data) <= limit {
			continue
		}
		if len(b.Tail) == 0 {
			from := len(b.Data) - tailSize
			if from < 0 {
				from = 0
			}
			b.Tail = append([]byte(nil), b.Data[from:]...)
		}
		b.Data = append([]byte(nil), b.Data[:limit]...)
		b.Truncated = true
	}
	e.mu.Unlock()
	e.rev.Add(1)
	e.store.touch()
}

// Snapshot returns a deep copy safe to use without locking.
func (e *Exchange) Snapshot() Record { return e.SnapshotLimit(-1) }

// SnapshotLimit is Snapshot, but copies at most max bytes of each body (the
// head; Total still reports the real size). Viewers that only display a prefix
// avoid copying megabytes on every refresh. max < 0 means no limit.
func (e *Exchange) SnapshotLimit(max int) Record {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r := e.rec
	r.Client.ReqHeader = e.rec.Client.ReqHeader.Clone()
	r.Client.RespHeader = e.rec.Client.RespHeader.Clone()
	r.Upstream.ReqHeader = e.rec.Upstream.ReqHeader.Clone()
	r.Upstream.RespHeader = e.rec.Upstream.RespHeader.Clone()
	r.Client.ReqBody = e.rec.Client.ReqBody.cloneLimit(max)
	r.Client.RespBody = e.rec.Client.RespBody.cloneLimit(max)
	r.Upstream.ReqBody = e.rec.Upstream.ReqBody.cloneLimit(max)
	r.Upstream.RespBody = e.rec.Upstream.RespBody.cloneLimit(max)
	return r
}

// Summary is the lightweight row data shown in the list.
type Summary struct {
	ID         uint64
	Started    time.Time
	Duration   time.Duration
	Done       bool
	Method     string
	Path       string
	Status     int
	VHost      string
	Dialect    string
	Backend    string
	Operation  string
	Mode       string
	Model      string
	Stream     bool
	Prompt     int
	Completion int
	Error      bool
	// Search is a lower-cased haystack used by text filters.
	Search string
}

// Summary returns row data without copying bodies.
func (e *Exchange) Summary() Summary {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r := &e.rec
	s := Summary{
		ID: r.ID, Started: r.Started, Duration: r.Duration(), Done: r.Done,
		Method: r.Client.Method, Path: r.Client.URL, Status: r.Client.Status,
		VHost: r.VHost, Dialect: r.Dialect, Backend: r.Backend, Operation: r.Operation,
		Mode: r.Mode, Model: r.Model, Stream: r.Stream, Prompt: r.Prompt, Completion: r.Completion,
		Error: r.Error != "" || r.Client.Status >= 400,
	}
	s.Search = strings.ToLower(strings.Join([]string{
		r.Client.Method, r.Client.URL, r.VHost, r.Host, r.Dialect, r.Backend, r.Model, r.Operation, r.Error,
	}, " "))
	return s
}
