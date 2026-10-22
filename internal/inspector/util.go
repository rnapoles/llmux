package inspector

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

var sensitive = map[string]bool{
	"authorization": true, "proxy-authorization": true, "x-api-key": true,
	"api-key": true, "cookie": true, "set-cookie": true,
}

// RedactHeaders returns a copy of h with credentials partially masked.
func RedactHeaders(h http.Header) http.Header {
	out := h.Clone()
	for k, vv := range out {
		if !sensitive[strings.ToLower(k)] {
			continue
		}
		for i, v := range vv {
			vv[i] = mask(v)
		}
	}
	return out
}

func mask(v string) string {
	prefix := ""
	if i := strings.IndexByte(v, ' '); i > 0 && i < 10 {
		prefix, v = v[:i+1], v[i+1:]
	}
	if len(v) <= 12 {
		return prefix + "••••••••"
	}
	return prefix + v[:4] + "••••••••" + v[len(v)-4:]
}

// ScanUsage extracts token usage from a JSON, SSE or NDJSON payload,
// understanding both OpenAI ("usage") and Ollama ("eval_count") shapes.
func ScanUsage(body Body) (prompt, completion int) {
	data := body.Data
	if body.Truncated {
		data = body.Tail
	}
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) == 1 || json.Valid(bytes.TrimSpace(data)) {
		lines = [][]byte{data}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := bytes.TrimSpace(lines[i])
		l = bytes.TrimPrefix(l, []byte("data:"))
		l = bytes.TrimSpace(l)
		if len(l) == 0 || l[0] != '{' {
			continue
		}
		var v struct {
			Usage *struct {
				P int `json:"prompt_tokens"`
				C int `json:"completion_tokens"`
			} `json:"usage"`
			PromptEval int `json:"prompt_eval_count"`
			Eval       int `json:"eval_count"`
		}
		if json.Unmarshal(l, &v) != nil {
			continue
		}
		if v.Usage != nil && (v.Usage.P > 0 || v.Usage.C > 0) {
			return v.Usage.P, v.Usage.C
		}
		if v.PromptEval > 0 || v.Eval > 0 {
			return v.PromptEval, v.Eval
		}
	}
	return 0, 0
}
