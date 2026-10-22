// Package openai emulates the OpenAI REST API towards clients.
package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rnapoles/llmux/internal/core"
	"github.com/rnapoles/llmux/internal/httpx"
	wire "github.com/rnapoles/llmux/internal/wire/openai"
)

const Name = "openai"

type Dialect struct{}

func New() *Dialect { return &Dialect{} }

func (*Dialect) Name() string { return Name }

func (*Dialect) Match(r *http.Request) (core.Operation, bool) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case p == "/v1/chat/completions" || p == "/chat/completions":
		if r.Method == http.MethodPost {
			return core.OpChat, true
		}
		return core.OpOther, true
	case p == "/v1/models":
		if r.Method == http.MethodGet {
			return core.OpModels, true
		}
		return core.OpOther, true
	case strings.HasPrefix(p, "/v1/"):
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
		return nil, &core.BadRequestError{Msg: "you must provide a model parameter"}
	}
	if len(req.Messages) == 0 {
		return nil, &core.BadRequestError{Msg: "'messages' is a required property"}
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
	var body wire.ErrorBody
	body.Error.Message = err.Error()
	body.Error.Type = errorType(status)
	_ = httpx.WriteJSON(w, status, body)
}

func errorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "invalid_request_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "server_error"
	}
	return "invalid_request_error"
}

func (*Dialect) NewStreamEncoder(w http.ResponseWriter, req *core.ChatRequest) core.StreamEncoder {
	return &streamEncoder{w: w, req: req, id: core.NewID("chatcmpl-")}
}

// streamEncoder emits Server-Sent Events in OpenAI "chat.completion.chunk" format.
type streamEncoder struct {
	w       http.ResponseWriter
	req     *core.ChatRequest
	id      string
	created int64
	started bool
}

func (e *streamEncoder) Begin() error {
	h := e.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	e.w.WriteHeader(http.StatusOK)
	httpx.Flush(e.w)
	return nil
}

func (e *streamEncoder) emit(c wire.ChatResponse) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(e.w, "data: %s\n\n", b); err != nil {
		return err
	}
	httpx.Flush(e.w)
	return nil
}

func (e *streamEncoder) chunk(d *wire.Delta, finish *string) wire.ChatResponse {
	return wire.ChatResponse{
		ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.req.Model,
		Choices: []wire.Choice{{Index: 0, Delta: d, FinishReason: finish}},
	}
}

func (e *streamEncoder) Write(c core.StreamChunk) error {
	if e.created == 0 {
		e.created = time.Now().Unix()
	}
	if !e.started {
		e.started = true
		if err := e.emit(e.chunk(&wire.Delta{Role: "assistant"}, nil)); err != nil {
			return err
		}
	}
	if !c.Done {
		if c.Delta == "" {
			return nil
		}
		return e.emit(e.chunk(&wire.Delta{Content: c.Delta}, nil))
	}
	fr := c.FinishReason
	if fr == "" {
		fr = "stop"
	}
	if err := e.emit(e.chunk(&wire.Delta{}, &fr)); err != nil {
		return err
	}
	if e.req.IncludeUsage && c.Usage != nil {
		u := wire.ChatResponse{
			ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.req.Model,
			Choices: []wire.Choice{},
			Usage: &wire.Usage{
				PromptTokens: c.Usage.PromptTokens, CompletionTokens: c.Usage.CompletionTokens, TotalTokens: c.Usage.Total(),
			},
		}
		return e.emit(u)
	}
	return nil
}

func (e *streamEncoder) End() error {
	_, err := fmt.Fprint(e.w, "data: [DONE]\n\n")
	httpx.Flush(e.w)
	return err
}
