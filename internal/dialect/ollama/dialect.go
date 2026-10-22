// Package ollama emulates the Ollama REST API towards clients.
package ollama

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rnapoles/llmux/internal/core"
	"github.com/rnapoles/llmux/internal/httpx"
	wire "github.com/rnapoles/llmux/internal/wire/ollama"
)

const Name = "ollama"

type Dialect struct{ version string }

func New() *Dialect { return &Dialect{version: "0.5.0-llmux"} }

func (*Dialect) Name() string { return Name }

func (*Dialect) Match(r *http.Request) (core.Operation, bool) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case p == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		return core.OpInfo, true
	case p == "/api/version":
		return core.OpInfo, true
	case p == "/api/chat":
		if r.Method == http.MethodPost {
			return core.OpChat, true
		}
		return core.OpOther, true
	case p == "/api/tags":
		if r.Method == http.MethodGet {
			return core.OpModels, true
		}
		return core.OpOther, true
	case strings.HasPrefix(p, "/api/"):
		return core.OpOther, true
	}
	return "", false
}

func (*Dialect) DecodeChat(body []byte) (*core.ChatRequest, error) {
	var req wire.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, &core.BadRequestError{Msg: "invalid JSON body: " + err.Error()}
	}
	if req.Model == "" {
		return nil, &core.BadRequestError{Msg: "model is required"}
	}
	return req.ToCore(), nil
}

func (*Dialect) WriteChat(w http.ResponseWriter, _ *core.ChatRequest, resp *core.ChatResponse) error {
	return httpx.WriteJSON(w, http.StatusOK, wire.FromCoreResponse(resp))
}

func (*Dialect) WriteModels(w http.ResponseWriter, models []core.ModelInfo) error {
	return httpx.WriteJSON(w, http.StatusOK, wire.FromCoreModels(models))
}

func (*Dialect) WriteError(w http.ResponseWriter, status int, err error) {
	_ = httpx.WriteJSON(w, status, wire.ErrorBody{Error: err.Error()})
}

// WriteInfo implements core.InfoWriter.
func (d *Dialect) WriteInfo(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSuffix(r.URL.Path, "/") == "/api/version" {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"version": d.version})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("Ollama is running"))
	}
}

func (*Dialect) NewStreamEncoder(w http.ResponseWriter, req *core.ChatRequest) core.StreamEncoder {
	return &streamEncoder{w: w, req: req, start: time.Now()}
}

// streamEncoder emits newline-delimited JSON, Ollama's streaming format.
type streamEncoder struct {
	w     http.ResponseWriter
	req   *core.ChatRequest
	start time.Time
}

func (e *streamEncoder) Begin() error {
	e.w.Header().Set("Content-Type", "application/x-ndjson")
	e.w.Header().Set("Cache-Control", "no-cache")
	e.w.WriteHeader(http.StatusOK)
	httpx.Flush(e.w)
	return nil
}

func (e *streamEncoder) Write(c core.StreamChunk) error {
	role := c.Role
	if role == "" {
		role = "assistant"
	}
	msg := wire.ChatResponse{
		Model:     e.req.Model,
		CreatedAt: time.Now().UTC(),
		Message:   wire.Message{Role: role, Content: c.Delta},
		Done:      c.Done,
	}
	if c.Done {
		msg.DoneReason = c.FinishReason
		if msg.DoneReason == "" {
			msg.DoneReason = "stop"
		}
		msg.TotalDuration = time.Since(e.start).Nanoseconds()
		if c.Usage != nil {
			msg.PromptEvalCount = c.Usage.PromptTokens
			msg.EvalCount = c.Usage.CompletionTokens
		}
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err = e.w.Write(append(b, '\n')); err != nil {
		return err
	}
	httpx.Flush(e.w)
	return nil
}

func (e *streamEncoder) End() error { return nil }
