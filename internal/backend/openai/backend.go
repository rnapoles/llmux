// Package openai is a backend for OpenAI and OpenAI-compatible servers
// (OpenAI, vLLM, LM Studio, llama.cpp, OpenRouter, Ollama's /v1, ...).
package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rnapoles/llmux/internal/core"
	"github.com/rnapoles/llmux/internal/upstream"
	wire "github.com/rnapoles/llmux/internal/wire/openai"
)

const Name = "openai"

type Backend struct {
	client *upstream.Client
	apiKey string
}

// Factory implements core.BackendFactory.
func Factory(spec core.BackendSpec) (core.Backend, error) {
	return &Backend{client: &upstream.Client{Base: spec.BaseURL, HTTP: spec.Client}, apiKey: spec.APIKey}, nil
}

func (b *Backend) Name() string       { return Name }
func (b *Backend) Dialect() string    { return "openai" }
func (b *Backend) Endpoint() *url.URL { return b.client.Base }

func (b *Backend) PrepareHeaders(h http.Header) {
	if b.apiKey != "" && h.Get("Authorization") == "" {
		h.Set("Authorization", "Bearer "+b.apiKey)
	}
}

func (b *Backend) Chat(ctx context.Context, req *core.ChatRequest, opts core.CallOptions) (*core.ChatResponse, error) {
	wr := wire.FromCoreRequest(req)
	wr.Stream = false
	resp, err := b.client.Do(ctx, http.MethodPost, "/v1/chat/completions", wr, opts.Header)
	if err != nil {
		return nil, err
	}
	var out wire.ChatResponse
	if err := upstream.DecodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return out.ToCore(), nil
}

func (b *Backend) ChatStream(ctx context.Context, req *core.ChatRequest, opts core.CallOptions) (core.StreamReader, error) {
	wr := wire.FromCoreRequest(req)
	wr.Stream = true
	wr.StreamOptions = &wire.StreamOptions{IncludeUsage: true}
	h := opts.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Accept", "text/event-stream")
	resp, err := b.client.Do(ctx, http.MethodPost, "/v1/chat/completions", wr, h)
	if err != nil {
		return nil, err
	}
	return &streamReader{body: resp.Body, r: bufio.NewReader(resp.Body)}, nil
}

func (b *Backend) Models(ctx context.Context, opts core.CallOptions) ([]core.ModelInfo, error) {
	resp, err := b.client.Do(ctx, http.MethodGet, "/v1/models", nil, opts.Header)
	if err != nil {
		return nil, err
	}
	var out wire.ModelList
	if err := upstream.DecodeJSON(resp, &out); err != nil {
		return nil, err
	}
	return out.ToCore(), nil
}

// streamReader converts an SSE stream into neutral chunks.
type streamReader struct {
	body   io.ReadCloser
	r      *bufio.Reader
	finish string
	usage  *core.Usage
	done   bool
}

func (s *streamReader) Close() error { return s.body.Close() }

func (s *streamReader) final() core.StreamChunk {
	s.done = true
	return core.StreamChunk{Done: true, FinishReason: s.finish, Usage: s.usage}
}

func (s *streamReader) Next() (core.StreamChunk, error) {
	for {
		if s.done {
			return core.StreamChunk{}, io.EOF
		}
		line, err := s.r.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF {
				return s.final(), nil
			}
			return core.StreamChunk{}, err
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		switch data {
		case "":
			continue
		case "[DONE]":
			return s.final(), nil
		}
		var c wire.ChatResponse
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return core.StreamChunk{}, err
		}
		if c.Usage != nil {
			s.usage = &core.Usage{PromptTokens: c.Usage.PromptTokens, CompletionTokens: c.Usage.CompletionTokens}
		}
		if len(c.Choices) == 0 {
			continue
		}
		ch := c.Choices[0]
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			s.finish = *ch.FinishReason
		}
		if ch.Delta != nil && (ch.Delta.Content != "" || ch.Delta.Role != "") {
			return core.StreamChunk{Role: ch.Delta.Role, Delta: ch.Delta.Content}, nil
		}
	}
}
