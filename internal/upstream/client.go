// Package upstream is a small JSON-over-HTTP client shared by all backends.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rnapoles/llmux/internal/core"
)

type Client struct {
	Base *url.URL
	HTTP *http.Client
}

// URL joins the base URL with an API path.
func (c *Client) URL(path string) string {
	u := *c.Base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	return u.String()
}

// Do sends a JSON request. Non-2xx answers become *core.UpstreamError.
func (c *Client) Do(ctx context.Context, method, path string, body any, h http.Header) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path), rdr)
	if err != nil {
		return nil, err
	}
	for k, vv := range h {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, &core.UpstreamError{Status: resp.StatusCode, Message: ErrorMessage(b), Body: b}
	}
	return resp, nil
}

// DecodeJSON reads and closes resp.Body, decoding into v.
func DecodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// ErrorMessage extracts a human-readable message from common error envelopes.
func ErrorMessage(body []byte) string {
	var generic struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &generic) == nil && len(generic.Error) > 0 {
		var s string
		if json.Unmarshal(generic.Error, &s) == nil {
			return s
		}
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(generic.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		msg = "empty response"
	}
	return msg
}
