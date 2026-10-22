package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/rnapoles/llmux/internal/inspector"
)

// captureWriter records what is sent to the client while staying transparent
// for streaming (Flush) and http.ResponseController (Unwrap).
type captureWriter struct {
	http.ResponseWriter
	ex     *inspector.Exchange
	status int
	wrote  bool
	redact bool
}

func newCaptureWriter(w http.ResponseWriter, ex *inspector.Exchange, redact bool) *captureWriter {
	return &captureWriter{ResponseWriter: w, ex: ex, status: http.StatusOK, redact: redact}
}

func (c *captureWriter) WriteHeader(code int) {
	if c.wrote {
		return
	}
	c.wrote = true
	c.status = code
	h := c.Header().Clone()
	ct := h.Get("Content-Type")
	stream := strings.Contains(ct, "text/event-stream") || strings.Contains(ct, "ndjson")
	if c.redact {
		h = inspector.RedactHeaders(h)
	}
	c.ex.Update(func(r *inspector.Record) {
		r.Client.Status = code
		r.Client.RespHeader = h
		r.Stream = r.Stream || stream
	})
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	c.ex.Update(func(r *inspector.Record) {
		if r.FirstByte.IsZero() {
			r.FirstByte = time.Now()
		}
	})
	n, err := c.ResponseWriter.Write(p)
	c.ex.AppendBody(func(r *inspector.Record) *inspector.Body { return &r.Client.RespBody }, p[:n])
	return n, err
}

func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
