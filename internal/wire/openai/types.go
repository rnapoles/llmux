// Package openai defines the OpenAI wire format and its mapping to the
// neutral core model. It is shared by the OpenAI dialect (server side) and
// the OpenAI backend (client side) so the format is described exactly once.
package openai

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/rnapoles/llmux/internal/core"
)

// Content accepts either a plain string or an array of typed parts and keeps
// only the concatenated text. It always marshals as a plain string.
type Content string

func (c *Content) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*c = Content(s)
		return nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &parts); err != nil {
		return err
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "" || p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	*c = Content(sb.String())
	return nil
}

// StringList accepts a string or an array of strings (the "stop" field).
type StringList []string

func (l *StringList) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s != "" {
			*l = StringList{s}
		}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*l = arr
	return nil
}

type Message struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatRequest struct {
	Model               string         `json:"model"`
	Messages            []Message      `json:"messages"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *StreamOptions `json:"stream_options,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	TopP                *float64       `json:"top_p,omitempty"`
	MaxTokens           *int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
	Stop                StringList     `json:"stop,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	Delta        *Delta   `json:"delta,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

// ChatResponse doubles as the streaming chunk type ("chat.completion.chunk").
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type ErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func (r ChatRequest) ToCore() *core.ChatRequest {
	out := &core.ChatRequest{
		Model:       r.Model,
		Stream:      r.Stream,
		Temperature: r.Temperature,
		TopP:        r.TopP,
		MaxTokens:   r.MaxTokens,
		Stop:        []string(r.Stop),
	}
	if out.MaxTokens == nil {
		out.MaxTokens = r.MaxCompletionTokens
	}
	if r.StreamOptions != nil {
		out.IncludeUsage = r.StreamOptions.IncludeUsage
	}
	for _, m := range r.Messages {
		out.Messages = append(out.Messages, core.Message{Role: m.Role, Content: string(m.Content)})
	}
	return out
}

func FromCoreRequest(r *core.ChatRequest) ChatRequest {
	out := ChatRequest{
		Model:       r.Model,
		Stream:      r.Stream,
		Temperature: r.Temperature,
		TopP:        r.TopP,
		MaxTokens:   r.MaxTokens,
		Stop:        StringList(r.Stop),
	}
	for _, m := range r.Messages {
		out.Messages = append(out.Messages, Message{Role: m.Role, Content: Content(m.Content)})
	}
	return out
}

func FromCoreResponse(r *core.ChatResponse) ChatResponse {
	created := r.Created
	if created.IsZero() {
		created = time.Now()
	}
	id := r.ID
	if id == "" {
		id = core.NewID("chatcmpl-")
	}
	fr := r.FinishReason
	if fr == "" {
		fr = "stop"
	}
	return ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created.Unix(),
		Model:   r.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      &Message{Role: orDefault(r.Message.Role, "assistant"), Content: Content(r.Message.Content)},
			FinishReason: &fr,
		}},
		Usage: &Usage{
			PromptTokens:     r.Usage.PromptTokens,
			CompletionTokens: r.Usage.CompletionTokens,
			TotalTokens:      r.Usage.Total(),
		},
	}
}

func (r ChatResponse) ToCore() *core.ChatResponse {
	out := &core.ChatResponse{
		ID:      r.ID,
		Model:   r.Model,
		Created: time.Unix(r.Created, 0),
	}
	if len(r.Choices) > 0 {
		c := r.Choices[0]
		if c.Message != nil {
			out.Message = core.Message{Role: c.Message.Role, Content: string(c.Message.Content)}
		}
		if c.FinishReason != nil {
			out.FinishReason = *c.FinishReason
		}
	}
	if r.Usage != nil {
		out.Usage = core.Usage{PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens}
	}
	return out
}

func (l ModelList) ToCore() []core.ModelInfo {
	out := make([]core.ModelInfo, 0, len(l.Data))
	for _, m := range l.Data {
		out = append(out, core.ModelInfo{ID: m.ID, OwnedBy: m.OwnedBy, Created: time.Unix(m.Created, 0)})
	}
	return out
}

func FromCoreModels(models []core.ModelInfo) ModelList {
	out := ModelList{Object: "list", Data: make([]Model, 0, len(models))}
	for _, m := range models {
		created := m.Created
		if created.IsZero() {
			created = time.Now()
		}
		out.Data = append(out.Data, Model{ID: m.ID, Object: "model", Created: created.Unix(), OwnedBy: orDefault(m.OwnedBy, "llmux")})
	}
	return out
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
