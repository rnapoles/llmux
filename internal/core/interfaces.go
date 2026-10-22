package core

import (
	"context"
	"net/http"
	"net/url"
)

// ---------------------------------------------------------------------------
// Dialects: the APIs LLMux *emulates* towards its clients.
// Interfaces are deliberately small (Interface Segregation) and composed
// into Dialect.
// ---------------------------------------------------------------------------

// Matcher recognises which requests belong to a dialect.
type Matcher interface {
	// Match reports whether the request belongs to this dialect and, if so,
	// what it means in neutral terms.
	Match(r *http.Request) (Operation, bool)
}

// ChatCodec translates chat payloads between the dialect wire format and the
// neutral model.
type ChatCodec interface {
	DecodeChat(body []byte) (*ChatRequest, error)
	WriteChat(w http.ResponseWriter, req *ChatRequest, resp *ChatResponse) error
	NewStreamEncoder(w http.ResponseWriter, req *ChatRequest) StreamEncoder
}

// ModelsCodec renders a model list in the dialect wire format.
type ModelsCodec interface {
	WriteModels(w http.ResponseWriter, models []ModelInfo) error
}

// ErrorWriter renders errors in the dialect's native error envelope.
type ErrorWriter interface {
	WriteError(w http.ResponseWriter, status int, err error)
}

// InfoWriter is an optional capability for dialects that expose liveness or
// version endpoints (e.g. Ollama's "/" and "/api/version").
type InfoWriter interface {
	WriteInfo(w http.ResponseWriter, r *http.Request)
}

// Dialect is a complete client-facing API.
type Dialect interface {
	Name() string
	Matcher
	ChatCodec
	ModelsCodec
	ErrorWriter
}

// StreamEncoder writes a streamed answer to the client in dialect format.
type StreamEncoder interface {
	Begin() error
	Write(chunk StreamChunk) error
	End() error
}

// ---------------------------------------------------------------------------
// Backends: the upstream servers LLMux *talks to*.
// ---------------------------------------------------------------------------

// CallOptions carries per-call data computed by the gateway.
type CallOptions struct {
	// Header is the complete set of outbound headers (already forwarded,
	// filtered, overridden and authorised by the gateway).
	Header http.Header
}

// StreamReader yields neutral chunks from an upstream stream. Next returns
// io.EOF once the final (Done) chunk has been delivered.
type StreamReader interface {
	Next() (StreamChunk, error)
	Close() error
}

// ChatBackend performs chat completions.
type ChatBackend interface {
	Chat(ctx context.Context, req *ChatRequest, opts CallOptions) (*ChatResponse, error)
	ChatStream(ctx context.Context, req *ChatRequest, opts CallOptions) (StreamReader, error)
}

// ModelLister lists upstream models.
type ModelLister interface {
	Models(ctx context.Context, opts CallOptions) ([]ModelInfo, error)
}

// Backend is a complete upstream provider.
type Backend interface {
	// Name is the provider type, e.g. "openai".
	Name() string
	// Dialect is the name of the native dialect this server speaks. When a
	// client dialect equals it, the gateway proxies the request transparently.
	Dialect() string
	// Endpoint is the upstream base URL.
	Endpoint() *url.URL
	// PrepareHeaders adds provider-required headers (authentication,
	// version pins...) if the caller has not already set them.
	PrepareHeaders(h http.Header)

	ChatBackend
	ModelLister
}

// BackendSpec is everything a factory needs to build a Backend.
type BackendSpec struct {
	BaseURL *url.URL
	APIKey  string
	Client  *http.Client
}

// BackendFactory builds a Backend from a spec.
type BackendFactory func(spec BackendSpec) (Backend, error)
