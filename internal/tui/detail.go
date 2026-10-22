package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/rnapoles/llmux/internal/inspector"
	wire "github.com/rnapoles/llmux/internal/wire/openai"
)

type tabID int

const (
	tabOverview tabID = iota
	tabRequest
	tabResponse
	tabUpReq
	tabUpRes
	tabCount
)

var tabNames = [tabCount][2]string{
	{"Overview", "Info"},
	{"Request", "Req"},
	{"Response", "Res"},
	{"Upstream Req", "Up▸"},
	{"Upstream Res", "◂Up"},
}

// buildDetail renders the selected exchange for a tab into display lines.
func buildDetail(rec inspector.Record, tab tabID, raw, wrap bool, w int) []string {
	var sb strings.Builder
	switch tab {
	case tabOverview:
		overview(&sb, rec, w)
	case tabRequest:
		leg(&sb, "Client → LLMux", rec.Client.Method+" "+rec.Client.URL, rec.Client.ReqHeader, rec.Client.ReqBody, raw, w, true)
	case tabResponse:
		status := "pending…"
		if rec.Client.Status != 0 {
			status = fmt.Sprintf("HTTP %d %s", rec.Client.Status, http.StatusText(rec.Client.Status))
		}
		leg(&sb, "LLMux → Client", status, rec.Client.RespHeader, rec.Client.RespBody, raw, w, true)
	case tabUpReq:
		if rec.Upstream.Method == "" {
			sb.WriteString(fg(cDim).Render("  No upstream call was made for this exchange.") + "\n")
			break
		}
		leg(&sb, "LLMux → Upstream", rec.Upstream.Method+" "+rec.Upstream.URL, rec.Upstream.ReqHeader, rec.Upstream.ReqBody, raw, w, true)
	case tabUpRes:
		if rec.Upstream.Method == "" {
			sb.WriteString(fg(cDim).Render("  No upstream call was made for this exchange.") + "\n")
			break
		}
		status := "pending…"
		if rec.Upstream.Status != 0 {
			status = fmt.Sprintf("HTTP %d %s", rec.Upstream.Status, http.StatusText(rec.Upstream.Status))
		}
		leg(&sb, "Upstream → LLMux", status, rec.Upstream.RespHeader, rec.Upstream.RespBody, raw, w, true)
	}
	text := strings.TrimRight(sb.String(), "\n")
	if wrap {
		text = lipgloss.NewStyle().Width(w).Render(text)
	} else {
		text = lipgloss.NewStyle().MaxWidth(w).Render(text)
	}
	return strings.Split(text, "\n")
}

func section(title string, w int) string {
	t := " " + title + " "
	n := w - lipgloss.Width(t) - 3
	if n < 2 {
		n = 2
	}
	return fg(cViolet).Render("──"+t) + fg(cBorder).Render(strings.Repeat("─", n)) + "\n"
}

func kv(sb *strings.Builder, k, v string) {
	sb.WriteString(fg(cDim).Render(fmt.Sprintf("  %-9s", k)) + " " + v + "\n")
}

func overview(sb *strings.Builder, r inspector.Record, w int) {
	code := r.Client.Status
	st := "in flight"
	if code != 0 {
		st = fmt.Sprintf("%d %s", code, http.StatusText(code))
	}
	title := bold(methodColor(r.Client.Method)).Render(r.Client.Method) + " " + bold(cText).Render(r.Client.URL)
	sb.WriteString("\n  " + title + "\n")
	sb.WriteString("  " + bold(statusColor(code, r.Done)).Render("● "+st) +
		fg(cDim).Render("   "+fmtDur(r.Duration())) + "\n\n")

	route := fg(cAccent).Render(orDash(r.Dialect)) + fg(cDim).Render("  ⇄  ") + fg(cViolet).Render(orDash(r.Backend))
	mode := map[string]string{"proxy": "transparent proxy", "translate": "protocol translation"}[r.Mode]
	if mode != "" {
		route += fg(cDim).Render("   (" + mode + ")")
	}
	kv(sb, "Route", route)
	kv(sb, "VHost", orDash(r.VHost)+fg(cDim).Render("   Host: "+r.Host))
	kv(sb, "Model", bold(cText).Render(orDash(r.Model)))
	stream := "no"
	if r.Stream {
		stream = fg(cPink).Render("yes ≈")
		if !r.FirstByte.IsZero() {
			stream += fg(cDim).Render("   first byte after " + fmtDur(r.FirstByte.Sub(r.Started)))
		}
	}
	kv(sb, "Stream", stream)
	kv(sb, "Tokens", fmt.Sprintf("%s %d prompt   %s %d completion   %s %d total",
		fg(cBlue).Render("↑"), r.Prompt, fg(cGreen).Render("↓"), r.Completion, fg(cAmber).Render("Σ"), r.Prompt+r.Completion))
	kv(sb, "Client", r.ClientAddr)
	kv(sb, "Started", r.Started.Format("2006-01-02 15:04:05.000"))
	if r.Upstream.Method != "" {
		us := "…"
		if r.Upstream.Status != 0 {
			us = fmt.Sprint(r.Upstream.Status)
		}
		kv(sb, "Upstream", trunc(r.Upstream.Method+" "+r.Upstream.URL, w-16)+fg(cDim).Render("  → ")+us)
	}
	kv(sb, "Payload", fmt.Sprintf("req %s · res %s", fmtBytes(r.Client.ReqBody.Total), fmtBytes(r.Client.RespBody.Total)))
	if r.Error != "" {
		kv(sb, "Error", bold(cRed).Render(r.Error))
	}

	turns := requestTurns(r.Client.ReqBody.Data)
	if reply := responseText(r.Client.RespBody); reply != "" {
		turns = append(turns, turn{"assistant", reply})
	}
	if len(turns) > 0 {
		sb.WriteString("\n" + section("Conversation", w))
		for _, t := range turns {
			col := cBlue
			switch t.role {
			case "assistant":
				col = cGreen
			case "system":
				col = cAmber
			}
			sb.WriteString("\n  " + bold(col).Render("▎"+strings.ToUpper(t.role)) + "\n")
			for _, ln := range strings.Split(strings.TrimSpace(sanitize(t.text)), "\n") {
				sb.WriteString("  " + ln + "\n")
			}
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func leg(sb *strings.Builder, title, line string, h http.Header, body inspector.Body, raw bool, w int, _ bool) {
	sb.WriteString("\n  " + bold(cAccent).Render(title) + "\n  " + bold(cText).Render(sanitize(line)) + "\n\n")
	sb.WriteString(section("Headers", w))
	if len(h) == 0 {
		sb.WriteString(fg(cDim).Render("  (none)") + "\n")
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			sb.WriteString("  " + fg(cBlue).Render(k+":") + " " + sanitize(v) + "\n")
		}
	}
	sb.WriteString("\n" + section(fmt.Sprintf("Body · %s", fmtBytes(body.Total)), w))
	sb.WriteString(indent(renderBody(body, raw), "  "))
	if body.Truncated {
		sb.WriteString("\n" + fg(cAmber).Render(fmt.Sprintf("  … truncated: showing first %s of %s", fmtBytes(int64(len(body.Data))), fmtBytes(body.Total))) + "\n")
	}
}

func indent(s, pre string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pre + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// renderBody formats JSON, SSE and NDJSON payloads.
func renderBody(b inspector.Body, raw bool) string {
	if b.Total == 0 {
		return fg(cDim).Render("  (empty)") + "\n"
	}
	text := sanitize(string(b.Data))
	if raw {
		return text + "\n"
	}
	if p, ok := prettyJSON([]byte(text)); ok {
		return colorizeJSON(p) + "\n"
	}
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return colorizeJSON(text) + "\n" // truncated JSON: highlight best-effort
	}
	if isEventStream(text) {
		var sb strings.Builder
		for _, ln := range strings.Split(text, "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			payload, prefix := ln, ""
			if strings.HasPrefix(ln, "data:") {
				payload, prefix = strings.TrimSpace(strings.TrimPrefix(ln, "data:")), sgrDim+"data: "+sgrReset
			}
			if c, ok := compactJSON([]byte(payload)); ok {
				sb.WriteString(prefix + colorizeJSON(c) + "\n")
			} else {
				sb.WriteString(prefix + payload + "\n")
			}
		}
		return sb.String()
	}
	return text + "\n"
}

func isEventStream(s string) bool {
	if strings.HasPrefix(s, "data:") || strings.HasPrefix(s, "event:") || strings.Contains(s, "\ndata:") {
		return true
	}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 2 {
		return false
	}
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "{") {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Conversation extraction (best effort; understands OpenAI and Ollama).
// ---------------------------------------------------------------------------

type turn struct{ role, text string }

func requestTurns(body []byte) []turn {
	var v struct {
		System   string `json:"system"`
		Prompt   string `json:"prompt"`
		Messages []struct {
			Role    string       `json:"role"`
			Content wire.Content `json:"content"`
		} `json:"messages"`
	}
	if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, &v) != nil {
		return nil
	}
	var out []turn
	if v.System != "" {
		out = append(out, turn{"system", v.System})
	}
	for _, m := range v.Messages {
		out = append(out, turn{m.Role, string(m.Content)})
	}
	if v.Prompt != "" {
		out = append(out, turn{"user", v.Prompt})
	}
	return out
}

type replyChunk struct {
	Choices []struct {
		Message *struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta *struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Message *struct {
		Content string `json:"content"`
	} `json:"message"`
	Response string `json:"response"`
}

func (c replyChunk) text() string {
	var sb strings.Builder
	for _, ch := range c.Choices {
		if ch.Message != nil {
			sb.WriteString(ch.Message.Content)
		}
		if ch.Delta != nil {
			sb.WriteString(ch.Delta.Content)
		}
	}
	if c.Message != nil {
		sb.WriteString(c.Message.Content)
	}
	sb.WriteString(c.Response)
	return sb.String()
}

// responseText assembles the assistant text from JSON, SSE or NDJSON bodies.
func responseText(b inspector.Body) string {
	data := strings.TrimSpace(string(b.Data))
	if data == "" {
		return ""
	}
	if data[0] == '{' {
		var one replyChunk
		if json.Unmarshal([]byte(data), &one) == nil {
			if t := one.text(); t != "" {
				return t
			}
		}
	}
	var sb strings.Builder
	for _, ln := range strings.Split(data, "\n") {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "data:"))
		if ln == "" || ln[0] != '{' {
			continue
		}
		var c replyChunk
		if json.Unmarshal([]byte(ln), &c) == nil {
			sb.WriteString(c.text())
		}
	}
	return sb.String()
}
