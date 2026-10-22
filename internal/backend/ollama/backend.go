// Package ollama is a backend for native Ollama servers.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/rnapoles/llmux/internal/core"
	"github.com/rnapoles/llmux/internal/upstream"
	wire "github.com/rnapoles/llmux/internal/wire/ollama"
)

const Name = "ollama"

type Backend struct {
	client *upstream.Client
	apiKey string
}

// Factory implements core.BackendFactory.
func Factory(spec core.BackendSpec) (core.Backend, error) {
	return &Backend{client: &upstream.Client{Base: spec.BaseURL, HTTP: spec.Client}, apiKey: spec.APIKey}, nil
}

func (b *Backend) Name() string       { return Name }
func (b *Backend) Dialect() string    { return "ollama" }
func (b *Backend) Endpoint() *url.URL { return b.client.Base }

// PrepareHeaders: Ollama has no auth of its own, but reverse proxies in front
// of it commonly expect a bearer token.
func (b *Backend) PrepareHeaders(h http.Header) {
	if b.apiKey != "" && h.Get("Authorization") == "" {
		h.Set("Authorization", "Bearer "+b.apiKey)
	}
}

func (b *Backend) Chat(ctx context.Context, req *core.ChatRequest, opts core.CallOptions) (*core.ChatResponse, error) {
	resp, err := b.client.Do(ctx, http.MethodPost, "/api/chat", wire.FromCoreRequest(req, false), opts.Header)
	if err != nil {
		return nil, err
	}
	var out wire.ChatResponse
	if err := upstream.DecodeJSON(resp, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, errors.New(out.Error)
	}
	return out.ToCore(), nil
}

func (b *Backend) ChatStream(ctx context.Context, req *core.ChatRequest, opts core.CallOptions) (core.StreamReader, error) {
	h := opts.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Accept", "application/x-ndjson")
	resp, err := b.client.Do(ctx, http.MethodPost, "/api/chat", wire.FromCoreRequest(req, true), h)
	if err != nil {
		return nil, err
	}
	return &streamReader{body: resp.Body, r: bufio.NewReader(resp.Body)}, nil
}

func (b *Backend) Models(ctx context.Context, opts core.CallOptions) ([]core.ModelInfo, error) {
	resp, err := b.client.Do(ctx, http.MethodGet, "/api/tags", nil, opts.Header)
	if err != nil {
		return nil, err
	}
	var out wire.TagsResponse
	if err := upstream.DecodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return out.ToCore(), nil
}

// streamReader converts NDJSON into neutral chunks.
type streamReader struct {
	body io.ReadCloser
	r    *bufio.Reader
	done bool
}

func (s *streamReader) Close() error { return s.body.Close() }

func (s *streamReader) Next() (core.StreamChunk, error) {
	for {
		if s.done {
			return core.StreamChunk{}, io.EOF
		}
		line, err := s.r.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if err == io.EOF {
				s.done = true
				return core.StreamChunk{Done: true}, nil
			}
			if err != nil {
				return core.StreamChunk{}, err
			}
			continue
		}
		var c wire.ChatResponse
		if jerr := json.Unmarshal(line, &c); jerr != nil {
			return core.StreamChunk{}, jerr
		}
		if c.Error != "" {
			return core.StreamChunk{}, errors.New(c.Error)
		}
		if c.Done {
			s.done = true
			return core.StreamChunk{
				Done:         true,
				FinishReason: c.DoneReason,
				Usage:        &core.Usage{PromptTokens: c.PromptEvalCount, CompletionTokens: c.EvalCount},
			}, nil
		}
		return core.StreamChunk{Role: c.Message.Role, Delta: c.Message.Content}, nil
	}
}
