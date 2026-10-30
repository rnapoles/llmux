// Package app is the composition root: it wires configuration, registries,
// the gateway, the HTTP server and (optionally) the TUI together.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rnapoles/llmux/internal/backend"
	"github.com/rnapoles/llmux/internal/config"
	"github.com/rnapoles/llmux/internal/dialect"
	"github.com/rnapoles/llmux/internal/gateway"
	"github.com/rnapoles/llmux/internal/inspector"
	sqlitestore "github.com/rnapoles/llmux/internal/storage/sqlite"
	"github.com/rnapoles/llmux/internal/tui"
)

// Run starts LLMux and blocks until it stops.
func Run(cfg config.Config, version string) error {
	logger, closeLog, err := newLogger(cfg)
	if err != nil {
		return err
	}
	defer closeLog()

	// Recording is needed by the TUI, by the inspector option and by persistence.
	sq := cfg.Storage.SQLite
	var (
		store   *inspector.Store
		archive gateway.Archiver
	)
	if cfg.TUI || cfg.Inspector.Enabled || sq.Enabled {
		history, limit := cfg.Inspector.History, cfg.Inspector.BodyLimit.Int()
		if !cfg.TUI && !cfg.Inspector.Enabled {
			history = 1 // headless: nothing browses the in-memory history
		}
		if sq.Enabled && sq.BodyLimit.Int() > limit {
			limit = sq.BodyLimit.Int() // capture enough for the database
		}
		store = inspector.NewStore(history, limit)
	}
	if sq.Enabled {
		db, err := sqlitestore.Open(sqlitestore.Options{
			Path: sq.Path, QueueSize: sq.QueueSize, Redact: sq.Redact, Retention: sq.Retention, Logger: logger,
		})
		if err != nil {
			return err
		}
		// Deferred before the server shutdown below, so it runs *after* in-flight
		// requests have finished and enqueued their records: nothing is lost.
		defer func() {
			if err := db.Close(); err != nil {
				logger.Error("closing sqlite", "err", err)
			}
		}()
		archive = db
		logger.Info("persisting exchanges", "sqlite", sq.Path, "body_limit", sq.BodyLimit.String(), "redact", sq.Redact)
	}

	gw, err := gateway.New(cfg, gateway.Deps{
		Dialects: dialect.Default(),
		Backends: backend.Default(),
		Store:    store,
		Archive:  archive,
		Logger:   logger,
	})
	if err != nil {
		return err
	}

	// Bind first so port errors surface before the TUI takes over the screen.
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
	}
	srv := &http.Server{
		Handler:           gw,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	logger.Info("llmux listening", "addr", ln.Addr().String(), "version", version)

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}

	if cfg.TUI {
		defer shutdown()
		info := tui.Info{Version: version, Listen: ln.Addr().String(), Redact: cfg.Inspector.Redact}
		if sq.Enabled {
			info.Persist = sq.Path
		}
		for _, v := range gw.VHosts() {
			names := make([]string, 0, len(v.Dialects))
			for _, d := range v.Dialects {
				names = append(names, d.Name())
			}
			info.Hosts = append(info.Hosts, tui.HostInfo{
				Name: v.Name, Patterns: v.Patterns, Dialects: names,
				Upstream: v.Backend.Endpoint().String(), Type: v.Backend.Name(),
			})
		}
		return tui.Run(store, info, errc)
	}

	printBanner(cfg, gw, ln.Addr().String(), version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdown()
		return nil
	}
}

func newLogger(cfg config.Config) (*slog.Logger, func(), error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(cfg.Server.LogLevel))); err != nil {
		lvl = slog.LevelInfo
	}
	var w io.Writer = os.Stderr
	closer := func() {}
	if cfg.TUI { // the TUI owns the terminal: log to a file or nowhere
		w = io.Discard
		if cfg.Server.LogFile != "" {
			f, err := os.OpenFile(cfg.Server.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, nil, err
			}
			w, closer = f, func() { _ = f.Close() }
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})), closer, nil
}

func printBanner(cfg config.Config, gw *gateway.Gateway, addr, version string) {
	fmt.Fprintf(os.Stderr, "\n  ◆ LLMux %s — listening on %s\n\n", version, addr)
	for _, v := range gw.VHosts() {
		ds := make([]string, 0, len(v.Dialects))
		for _, d := range v.Dialects {
			ds = append(ds, d.Name())
		}
		fmt.Fprintf(os.Stderr, "  %-14s %s  ⇄  %s (%s)\n", v.Name, strings.Join(ds, "+"), v.Backend.Endpoint(), v.Backend.Name())
		fmt.Fprintf(os.Stderr, "  %-14s hosts: %s\n", "", strings.Join(v.Patterns, ", "))
	}
	if sq := cfg.Storage.SQLite; sq.Enabled {
		fmt.Fprintf(os.Stderr, "\n  persisting every exchange to SQLite: %s\n", sq.Path)
	}
	fmt.Fprintf(os.Stderr, "\n  health: http://%s/healthz   (Ctrl+C to stop)\n\n", addr)
}
