// Package gateway is the HTTP pipeline: virtual-host resolution → dialect
// routing → authentication → (transparent proxy | neutral translation).
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/rnapoles/llmux/internal/config"
	"github.com/rnapoles/llmux/internal/core"
	"github.com/rnapoles/llmux/internal/inspector"
)

// Archiver receives every finished exchange for long-term storage. The record
// is a private deep copy. Implementations must not block the caller.
type Archiver interface {
	Archive(rec inspector.Record)
}

// Deps are the collaborators injected into the gateway.
type Deps struct {
	Dialects DialectSource
	Backends BackendSource
	Store    *inspector.Store // optional (nil disables recording)
	Archive  Archiver         // optional (requires Store); e.g. the SQLite writer
	Logger   *slog.Logger
}

type Gateway struct {
	table   *hostTable
	vhosts  []*VHost
	store   *inspector.Store
	archive Archiver
	uiLimit int // body bytes kept in memory after archiving
	log     *slog.Logger
	maxBody int64
}

// New builds a gateway from configuration.
func New(cfg config.Config, deps Deps) (*Gateway, error) {
	g := &Gateway{
		store:   deps.Store,
		archive: deps.Archive,
		uiLimit: cfg.Inspector.BodyLimit.Int(),
		log:     deps.Logger,
		maxBody: cfg.Server.MaxBodyBytes.Int64(),
	}
	if g.log == nil {
		g.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	for _, hc := range cfg.Hosts {
		v, err := newVHost(hc, deps.Dialects, deps.Backends)
		if err != nil {
			return nil, err
		}
		g.vhosts = append(g.vhosts, v)
	}
	var err error
	if g.table, err = newHostTable(g.vhosts); err != nil {
		return nil, err
	}
	return g, nil
}

// VHosts exposes the configured virtual hosts (for banners / the TUI).
func (g *Gateway) VHosts() []*VHost { return g.vhosts }

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
		return
	}

	start := time.Now()
	ex := g.store.Begin()
	cw := newCaptureWriter(w, ex)
	r = r.WithContext(inspector.WithExchange(r.Context(), ex))
	defer g.finish(ex, cw, r, start)

	body, err := io.ReadAll(http.MaxBytesReader(cw, r.Body, g.maxBody))
	g.recordRequest(ex, r, body)
	if err != nil {
		writeGenericError(cw, http.StatusRequestEntityTooLarge, "request body too large or unreadable: "+err.Error())
		return
	}

	vh := g.table.lookup(r.Host)
	if vh == nil {
		writeGenericError(cw, http.StatusNotFound, "no virtual host configured for "+r.Host)
		return
	}
	d, op := vh.route(r)
	if d == nil {
		ex.Update(func(rec *inspector.Record) { rec.VHost = vh.Name })
		writeGenericError(cw, http.StatusNotFound, fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
		return
	}
	mode := "translate"
	if d.Name() == vh.Backend.Dialect() {
		mode = "proxy"
	}
	ex.Update(func(rec *inspector.Record) {
		rec.VHost, rec.Dialect, rec.Backend, rec.Operation, rec.Mode = vh.Name, d.Name(), vh.Backend.Name(), string(op), mode
	})

	if !vh.Auth.Authorize(r) {
		cw.Header().Set("WWW-Authenticate", `Bearer realm="llmux"`)
		d.WriteError(cw, http.StatusUnauthorized, errors.New("invalid or missing API key"))
		return
	}

	if mode == "proxy" {
		vh.serveProxy(cw, r, d, body)
		return
	}
	g.serveTranslated(cw, r, vh, d, op, body, ex)
}

func (g *Gateway) recordRequest(ex *inspector.Exchange, r *http.Request, body []byte) {
	h := r.Header.Clone()
	h.Set("Host", r.Host)
	var peek struct {
		Model string `json:"model"`
	}
	if len(body) > 0 && body[0] == '{' {
		_ = json.Unmarshal(body, &peek)
	}
	ex.Update(func(rec *inspector.Record) {
		rec.ClientAddr = r.RemoteAddr
		rec.Host = r.Host
		rec.Model = peek.Model
		rec.Client.Method = r.Method
		rec.Client.URL = r.URL.RequestURI()
		rec.Client.ReqHeader = h
	})
	ex.AppendBody(func(rec *inspector.Record) *inspector.Body { return &rec.Client.ReqBody }, body)
}

func (g *Gateway) finish(ex *inspector.Exchange, cw *captureWriter, r *http.Request, start time.Time) {
	dur := time.Since(start)
	ex.Update(func(rec *inspector.Record) {
		rec.Done = true
		rec.Finished = time.Now()
		rec.Client.Status = cw.status
		if rec.Prompt == 0 && rec.Completion == 0 {
			rec.Prompt, rec.Completion = inspector.ScanUsage(rec.Client.RespBody)
		}
	})
	if g.archive != nil && ex != nil {
		g.archive.Archive(ex.Snapshot())
		// The database now owns the full payloads; keep only what the UI needs.
		ex.Trim(g.uiLimit)
	}
	lvl := slog.LevelInfo
	if cw.status >= 500 {
		lvl = slog.LevelError
	} else if cw.status >= 400 {
		lvl = slog.LevelWarn
	}
	g.log.Log(context.Background(), lvl, "request",
		"method", r.Method, "path", r.URL.Path, "host", r.Host, "status", cw.status,
		"duration", dur.Round(time.Millisecond))
}

// serveTranslated handles cross-dialect requests through the neutral model.
func (g *Gateway) serveTranslated(w http.ResponseWriter, r *http.Request, vh *VHost, d core.Dialect, op core.Operation, body []byte, ex *inspector.Exchange) {
	ctx := r.Context()
	opts := core.CallOptions{Header: vh.upstreamHeaders(r.Header)}

	switch op {
	case core.OpChat:
		req, err := d.DecodeChat(body)
		if err != nil {
			g.fail(w, d, err, ex)
			return
		}
		ex.Update(func(rec *inspector.Record) { rec.Model, rec.Stream = req.Model, req.Stream })
		up := *req
		up.Model = vh.mapModel(req.Model)
		if req.Stream {
			g.stream(ctx, w, d, vh, req, &up, opts, ex)
			return
		}
		resp, err := vh.Backend.Chat(ctx, &up, opts)
		if err != nil {
			g.fail(w, d, err, ex)
			return
		}
		resp.Model = req.Model
		ex.Update(func(rec *inspector.Record) {
			rec.Prompt, rec.Completion = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
		})
		vh.Resp.FilterResponse(w.Header())
		if err := d.WriteChat(w, req, resp); err != nil {
			g.log.Warn("write chat response", "err", err)
		}

	case core.OpModels:
		models, err := vh.Backend.Models(ctx, opts)
		if err != nil {
			g.fail(w, d, err, ex)
			return
		}
		have := map[string]bool{}
		for _, m := range models {
			have[m.ID] = true
		}
		for alias := range vh.ModelMap {
			if !have[alias] {
				models = append(models, core.ModelInfo{ID: alias, OwnedBy: "llmux"})
			}
		}
		vh.Resp.FilterResponse(w.Header())
		_ = d.WriteModels(w, models)

	case core.OpInfo:
		if iw, ok := d.(core.InfoWriter); ok {
			vh.Resp.FilterResponse(w.Header())
			iw.WriteInfo(w, r)
			return
		}
		fallthrough
	default:
		d.WriteError(w, http.StatusNotImplemented, fmt.Errorf(
			"%s %s is not supported when translating %s requests to a %s upstream",
			r.Method, r.URL.Path, d.Name(), vh.Backend.Dialect()))
	}
}

func (g *Gateway) stream(ctx context.Context, w http.ResponseWriter, d core.Dialect, vh *VHost,
	req, up *core.ChatRequest, opts core.CallOptions, ex *inspector.Exchange) {

	sr, err := vh.Backend.ChatStream(ctx, up, opts)
	if err != nil {
		g.fail(w, d, err, ex)
		return
	}
	defer sr.Close()

	vh.Resp.FilterResponse(w.Header())
	enc := d.NewStreamEncoder(w, req)
	if err := enc.Begin(); err != nil {
		return
	}
	for {
		chunk, err := sr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Headers are already out: report in the recorder and end the stream.
			ex.Update(func(rec *inspector.Record) { rec.Error = "stream: " + err.Error() })
			g.log.Warn("upstream stream failed", "err", err)
			break
		}
		if chunk.Done && chunk.Usage != nil {
			u := *chunk.Usage
			ex.Update(func(rec *inspector.Record) { rec.Prompt, rec.Completion = u.PromptTokens, u.CompletionTokens })
		}
		if err := enc.Write(chunk); err != nil {
			return // client went away
		}
	}
	_ = enc.End()
}

// fail maps an error to a status and writes it in the dialect's format.
func (g *Gateway) fail(w http.ResponseWriter, d core.Dialect, err error, ex *inspector.Exchange) {
	status := http.StatusBadGateway
	msg := err.Error()
	var ue *core.UpstreamError
	var bre *core.BadRequestError
	switch {
	case errors.As(err, &ue):
		status, msg = ue.Status, ue.Message
	case errors.As(err, &bre):
		status = http.StatusBadRequest
	case errors.Is(err, context.Canceled):
		status = 499
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	}
	ex.Update(func(rec *inspector.Record) { rec.Error = msg })
	d.WriteError(w, status, errors.New(msg))
}

func writeGenericError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "type": "llmux_error"}})
	_, _ = w.Write(b)
}
