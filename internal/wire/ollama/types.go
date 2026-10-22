// Package ollama defines the Ollama wire format and its mapping to the
// neutral core model (shared by dialect and backend).
package ollama

import (
	"time"

	"github.com/rnapoles/llmux/internal/core"
)

type Message struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type Options struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   *bool     `json:"stream,omitempty"`
	Options  *Options  `json:"options,omitempty"`
}

type ChatResponse struct {
	Model           string    `json:"model"`
	CreatedAt       time.Time `json:"created_at"`
	Message         Message   `json:"message"`
	Done            bool      `json:"done"`
	DoneReason      string    `json:"done_reason,omitempty"`
	TotalDuration   int64     `json:"total_duration,omitempty"`
	PromptEvalCount int       `json:"prompt_eval_count,omitempty"`
	EvalCount       int       `json:"eval_count,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type ModelTag struct {
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	ModifiedAt time.Time `json:"modified_at"`
	Size       int64     `json:"size"`
	Digest     string    `json:"digest"`
	Details    struct {
		Format string `json:"format,omitempty"`
		Family string `json:"family,omitempty"`
	} `json:"details"`
}

type TagsResponse struct {
	Models []ModelTag `json:"models"`
}

type ErrorBody struct {
	Error string `json:"error"`
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

// ToCore converts a request. Ollama streams by default.
func (r ChatRequest) ToCore() *core.ChatRequest {
	out := &core.ChatRequest{
		Model:        r.Model,
		Stream:       r.Stream == nil || *r.Stream,
		IncludeUsage: true,
	}
	if r.Options != nil {
		out.Temperature = r.Options.Temperature
		out.TopP = r.Options.TopP
		out.MaxTokens = r.Options.NumPredict
		out.Stop = r.Options.Stop
	}
	for _, m := range r.Messages {
		out.Messages = append(out.Messages, core.Message{Role: m.Role, Content: m.Content})
	}
	return out
}

func FromCoreRequest(r *core.ChatRequest, stream bool) ChatRequest {
	out := ChatRequest{Model: r.Model, Stream: &stream}
	if r.Temperature != nil || r.TopP != nil || r.MaxTokens != nil || len(r.Stop) > 0 {
		out.Options = &Options{Temperature: r.Temperature, TopP: r.TopP, NumPredict: r.MaxTokens, Stop: r.Stop}
	}
	for _, m := range r.Messages {
		out.Messages = append(out.Messages, Message{Role: m.Role, Content: m.Content})
	}
	return out
}

func FromCoreResponse(r *core.ChatResponse) ChatResponse {
	created := r.Created
	if created.IsZero() {
		created = time.Now()
	}
	role := r.Message.Role
	if role == "" {
		role = "assistant"
	}
	reason := r.FinishReason
	if reason == "" {
		reason = "stop"
	}
	return ChatResponse{
		Model:           r.Model,
		CreatedAt:       created.UTC(),
		Message:         Message{Role: role, Content: r.Message.Content},
		Done:            true,
		DoneReason:      reason,
		PromptEvalCount: r.Usage.PromptTokens,
		EvalCount:       r.Usage.CompletionTokens,
	}
}

func (r ChatResponse) ToCore() *core.ChatResponse {
	return &core.ChatResponse{
		Model:        r.Model,
		Created:      r.CreatedAt,
		Message:      core.Message{Role: r.Message.Role, Content: r.Message.Content},
		FinishReason: r.DoneReason,
		Usage:        core.Usage{PromptTokens: r.PromptEvalCount, CompletionTokens: r.EvalCount},
	}
}

func (t TagsResponse) ToCore() []core.ModelInfo {
	out := make([]core.ModelInfo, 0, len(t.Models))
	for _, m := range t.Models {
		out = append(out, core.ModelInfo{ID: m.Name, OwnedBy: "ollama", Created: m.ModifiedAt, Size: m.Size})
	}
	return out
}

func FromCoreModels(models []core.ModelInfo) TagsResponse {
	out := TagsResponse{Models: make([]ModelTag, 0, len(models))}
	for _, m := range models {
		mod := m.Created
		if mod.IsZero() {
			mod = time.Now()
		}
		t := ModelTag{Name: m.ID, Model: m.ID, ModifiedAt: mod.UTC(), Size: m.Size}
		out.Models = append(out.Models, t)
	}
	return out
}
