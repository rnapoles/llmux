// Package sqlite persists every proxied exchange (both HTTP legs, with full
// headers and bodies) into a SQLite database for later analysis.
//
// Writes are asynchronous: the request path only enqueues a snapshot into a
// bounded channel; one goroutine batches rows into transactions. If the queue
// is full the record is dropped (and counted) rather than slowing down LLM
// traffic.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rnapoles/llmux/internal/inspector"
)

type Options struct {
	Path      string
	QueueSize int
	Redact    bool          // mask credentials in stored headers
	Retention time.Duration // 0 = keep forever
	Logger    *slog.Logger
}

type Store struct {
	db   *sql.DB
	opts Options
	log  *slog.Logger
	sql  string

	mu     sync.RWMutex
	closed bool
	ch     chan inspector.Record
	done   chan struct{}

	saved   atomic.Uint64
	dropped atomic.Uint64
}

// Open creates (or upgrades) the database and starts the writer goroutine.
func Open(o Options) (*Store, error) {
	if o.Path == "" {
		return nil, errors.New("sqlite: empty path")
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 4096
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if dir := filepath.Dir(o.Path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open(driverName, dsn(o.Path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; avoids SQLITE_BUSY between our own connections
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open %s: %w", o.Path, err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(o.Path, 0o600) // traffic may contain prompts and credentials

	s := &Store{
		db: db, opts: o, log: o.Logger, sql: insertSQL(),
		ch: make(chan inspector.Record, o.QueueSize), done: make(chan struct{}),
	}
	go s.run()
	return s, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	switch {
	case v > schemaVersion:
		return fmt.Errorf("sqlite: database schema v%d is newer than this LLMux (v%d)", v, schemaVersion)
	case v == schemaVersion:
		return nil
	}
	for _, stmt := range schemaStatements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite: create schema: %w", err)
		}
	}
	_, err := db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// Archive enqueues a finished exchange. It never blocks. rec must not be
// modified by the caller afterwards (pass a Snapshot).
func (s *Store) Archive(rec inspector.Record) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- rec:
	default:
		if n := s.dropped.Add(1); n == 1 || n%100 == 0 {
			s.log.Warn("sqlite queue full, dropping exchange", "dropped_total", n)
		}
	}
}

// Saved and Dropped report counters since start.
func (s *Store) Saved() uint64   { return s.saved.Load() }
func (s *Store) Dropped() uint64 { return s.dropped.Load() }

// Path returns the database file.
func (s *Store) Path() string { return s.opts.Path }

// Close flushes queued exchanges and closes the database.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.ch)
	s.mu.Unlock()
	<-s.done
	if n := s.dropped.Load(); n > 0 {
		s.log.Warn("exchanges dropped because the queue was full", "count", n)
	}
	return s.db.Close()
}

func (s *Store) run() {
	defer close(s.done)
	var tick <-chan time.Time
	if s.opts.Retention > 0 {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		tick = t.C
		s.prune()
	}
	for {
		select {
		case rec, ok := <-s.ch:
			if !ok {
				return
			}
			batch := []inspector.Record{rec}
			open := true
		drain:
			for len(batch) < 64 {
				select {
				case r, ok := <-s.ch:
					if !ok {
						open = false
						break drain
					}
					batch = append(batch, r)
				default:
					break drain
				}
			}
			s.write(batch)
			if !open {
				return
			}
		case <-tick:
			s.prune()
		}
	}
}

func (s *Store) write(batch []inspector.Record) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.log.Error("sqlite begin", "err", err)
		return
	}
	stmt, err := tx.PrepareContext(ctx, s.sql)
	if err != nil {
		_ = tx.Rollback()
		s.log.Error("sqlite prepare", "err", err)
		return
	}
	defer stmt.Close()

	args := make([]any, len(columns))
	ok := 0
	for i := range batch {
		r := &batch[i]
		if s.opts.Redact {
			r.Client.ReqHeader = inspector.RedactHeaders(r.Client.ReqHeader)
			r.Client.RespHeader = inspector.RedactHeaders(r.Client.RespHeader)
			r.Upstream.ReqHeader = inspector.RedactHeaders(r.Upstream.ReqHeader)
			r.Upstream.RespHeader = inspector.RedactHeaders(r.Upstream.RespHeader)
		}
		for j, c := range columns {
			args[j] = c.val(r)
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			s.log.Error("sqlite insert", "err", err)
			continue
		}
		ok++
	}
	if err := tx.Commit(); err != nil {
		s.log.Error("sqlite commit", "err", err)
		return
	}
	s.saved.Add(uint64(ok))
}

func (s *Store) prune() {
	cutoff := time.Now().Add(-s.opts.Retention).UnixMilli()
	res, err := s.db.Exec(`DELETE FROM exchanges WHERE started_unix_ms < ?`, cutoff)
	if err != nil {
		s.log.Error("sqlite prune", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.log.Info("sqlite retention: deleted old exchanges", "rows", n)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
