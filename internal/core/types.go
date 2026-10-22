// Package core holds the provider-neutral domain model and the abstractions
// (interfaces) that every other package depends on. It imports nothing from
// the rest of the project, which keeps the dependency graph acyclic and lets
// new LLM APIs be added without touching existing code (Open/Closed).
package core

import "time"

// Operation is the provider-neutral meaning of an incoming HTTP request.
type Operation string

const (
	OpChat   Operation = "chat"   // chat completion (streaming or not)
	OpModels Operation = "models" // list available models
	OpInfo   Operation = "info"   // liveness / version probes
	OpOther  Operation = "other"  // known dialect path with no neutral mapping
)

// Message is a single chat message. Only text content is modelled.
type Message struct {
	Role    string
	Content string
}

// ChatRequest is the neutral representation of a chat completion request.
type ChatRequest struct {
	Model        string
	Messages     []Message
	Stream       bool
	IncludeUsage bool
	Temperature  *float64
	TopP         *float64
	MaxTokens    *int
	Stop         []string
}

// Usage reports token consumption.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Total returns prompt + completion tokens.
func (u Usage) Total() int { return u.PromptTokens + u.CompletionTokens }

// ChatResponse is the neutral representation of a non-streaming answer.
type ChatResponse struct {
	ID           string
	Model        string
	Created      time.Time
	Message      Message
	FinishReason string
	Usage        Usage
}

// StreamChunk is one increment of a streamed answer. A well-behaved
// StreamReader emits exactly one chunk with Done=true, always last.
type StreamChunk struct {
	Role         string
	Delta        string
	Done         bool
	FinishReason string // only meaningful when Done
	Usage        *Usage // only meaningful when Done
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID      string
	OwnedBy string
	Created time.Time
	Size    int64
}
