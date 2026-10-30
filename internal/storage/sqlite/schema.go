package sqlite

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rnapoles/llmux/internal/inspector"
)

const schemaVersion = 1

// column describes one column and how to derive its value from a record.
// The CREATE TABLE and INSERT statements are both generated from this list,
// so they can never drift apart.
type column struct {
	name string
	typ  string
	val  func(r *inspector.Record) any
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func headersJSON(h http.Header) string {
	if len(h) == 0 {
		return "{}"
	}
	b, err := json.Marshal(map[string][]string(h))
	if err != nil {
		return "{}"
	}
	return string(b)
}

// bodyValue stores UTF-8 payloads as TEXT (so SQLite's json_extract works on
// them) and anything else as a BLOB. Empty bodies are NULL.
func bodyValue(b inspector.Body) any {
	if b.Total == 0 || len(b.Data) == 0 {
		return nil
	}
	if utf8.Valid(b.Data) {
		return string(b.Data)
	}
	return b.Data
}

// bodyColumns emits the four columns describing one captured body.
func bodyColumns(prefix string, sel func(r *inspector.Record) inspector.Body) []column {
	return []column{
		{prefix + "_body", "BLOB", func(r *inspector.Record) any { return bodyValue(sel(r)) }},
		{prefix + "_body_size", "INTEGER", func(r *inspector.Record) any { return sel(r).Total }},
		{prefix + "_body_truncated", "INTEGER", func(r *inspector.Record) any {
			b := sel(r)
			return b2i(b.Truncated && int64(len(b.Data)) < b.Total)
		}},
	}
}

func buildColumns() []column {
	cols := []column{
		{"started_at", "TEXT NOT NULL", func(r *inspector.Record) any { return r.Started.UTC().Format(time.RFC3339Nano) }},
		{"started_unix_ms", "INTEGER NOT NULL", func(r *inspector.Record) any { return r.Started.UnixMilli() }},
		{"duration_ms", "INTEGER", func(r *inspector.Record) any { return ms(r.Duration()) }},
		{"first_byte_ms", "INTEGER", func(r *inspector.Record) any {
			if r.FirstByte.IsZero() {
				return nil
			}
			return ms(r.FirstByte.Sub(r.Started))
		}},
		{"client_addr", "TEXT", func(r *inspector.Record) any { return nullable(r.ClientAddr) }},
		{"host", "TEXT", func(r *inspector.Record) any { return nullable(r.Host) }},
		{"vhost", "TEXT", func(r *inspector.Record) any { return nullable(r.VHost) }},
		{"dialect", "TEXT", func(r *inspector.Record) any { return nullable(r.Dialect) }},
		{"backend", "TEXT", func(r *inspector.Record) any { return nullable(r.Backend) }},
		{"operation", "TEXT", func(r *inspector.Record) any { return nullable(r.Operation) }},
		{"mode", "TEXT", func(r *inspector.Record) any { return nullable(r.Mode) }},
		{"model", "TEXT", func(r *inspector.Record) any { return nullable(r.Model) }},
		{"stream", "INTEGER", func(r *inspector.Record) any { return b2i(r.Stream) }},
		{"method", "TEXT", func(r *inspector.Record) any { return nullable(r.Client.Method) }},
		{"path", "TEXT", func(r *inspector.Record) any { return nullable(r.Client.URL) }},
		{"status", "INTEGER", func(r *inspector.Record) any { return r.Client.Status }},
		{"error", "TEXT", func(r *inspector.Record) any { return nullable(r.Error) }},
		{"prompt_tokens", "INTEGER", func(r *inspector.Record) any { return r.Prompt }},
		{"completion_tokens", "INTEGER", func(r *inspector.Record) any { return r.Completion }},

		// client <-> LLMux
		{"req_headers", "TEXT", func(r *inspector.Record) any { return headersJSON(r.Client.ReqHeader) }},
	}
	cols = append(cols, bodyColumns("req", func(r *inspector.Record) inspector.Body { return r.Client.ReqBody })...)
	cols = append(cols, column{"res_headers", "TEXT", func(r *inspector.Record) any { return headersJSON(r.Client.RespHeader) }})
	cols = append(cols, bodyColumns("res", func(r *inspector.Record) inspector.Body { return r.Client.RespBody })...)

	// LLMux <-> upstream
	cols = append(cols,
		column{"up_method", "TEXT", func(r *inspector.Record) any { return nullable(r.Upstream.Method) }},
		column{"up_url", "TEXT", func(r *inspector.Record) any { return nullable(r.Upstream.URL) }},
		column{"up_status", "INTEGER", func(r *inspector.Record) any {
			if r.Upstream.Method == "" {
				return nil
			}
			return r.Upstream.Status
		}},
		column{"up_req_headers", "TEXT", func(r *inspector.Record) any { return headersJSON(r.Upstream.ReqHeader) }},
	)
	cols = append(cols, bodyColumns("up_req", func(r *inspector.Record) inspector.Body { return r.Upstream.ReqBody })...)
	cols = append(cols, column{"up_res_headers", "TEXT", func(r *inspector.Record) any { return headersJSON(r.Upstream.RespHeader) }})
	cols = append(cols, bodyColumns("up_res", func(r *inspector.Record) inspector.Body { return r.Upstream.RespBody })...)
	return cols
}

var columns = buildColumns()

func createTableSQL() string {
	var sb strings.Builder
	sb.WriteString("CREATE TABLE IF NOT EXISTS exchanges (\n  id INTEGER PRIMARY KEY AUTOINCREMENT")
	for _, c := range columns {
		sb.WriteString(",\n  " + c.name + " " + c.typ)
	}
	sb.WriteString("\n)")
	return sb.String()
}

func insertSQL() string {
	names := make([]string, len(columns))
	marks := make([]string, len(columns))
	for i, c := range columns {
		names[i], marks[i] = c.name, "?"
	}
	return "INSERT INTO exchanges (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
}

// Statements executed on a fresh database (schemaVersion 1).
var schemaStatements = []string{
	createTableSQL(),
	`CREATE INDEX IF NOT EXISTS idx_exchanges_started ON exchanges(started_unix_ms)`,
	`CREATE INDEX IF NOT EXISTS idx_exchanges_model   ON exchanges(model)`,
	`CREATE INDEX IF NOT EXISTS idx_exchanges_status  ON exchanges(status)`,
	`CREATE INDEX IF NOT EXISTS idx_exchanges_vhost   ON exchanges(vhost)`,
	// Light-weight browsing view without headers/bodies.
	`CREATE VIEW IF NOT EXISTS exchange_summary AS
	   SELECT id, started_at, duration_ms, first_byte_ms, vhost, dialect, backend, mode, model, stream,
	          method, path, status, up_status, prompt_tokens, completion_tokens,
	          prompt_tokens + completion_tokens AS total_tokens, error
	   FROM exchanges`,
}
