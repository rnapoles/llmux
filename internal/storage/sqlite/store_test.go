package sqlite

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rnapoles/llmux/internal/inspector"
)

func sample(started time.Time) inspector.Record {
	r := inspector.Record{
		ID: 1, Started: started, Finished: started.Add(1500 * time.Millisecond), FirstByte: started.Add(200 * time.Millisecond),
		Done: true, ClientAddr: "10.0.0.1:5000", Host: "api.local", VHost: "main", Dialect: "openai", Backend: "ollama",
		Operation: "chat", Mode: "translate", Model: "gpt-4o", Stream: true, Prompt: 12, Completion: 34,
	}
	r.Client = inspector.Leg{
		Method: "POST", URL: "/v1/chat/completions?x=1", Status: 200,
		ReqHeader:  http.Header{"Authorization": {"Bearer client-secret-key-123456"}, "Content-Type": {"application/json"}},
		RespHeader: http.Header{"Content-Type": {"text/event-stream"}},
	}
	r.Upstream = inspector.Leg{
		Method: "POST", URL: "http://up:11434/api/chat", Status: 200,
		ReqHeader:  http.Header{"Authorization": {"Bearer upstream-secret-key-7890"}, "X-Multi": {"a", "b"}},
		RespHeader: http.Header{"Content-Type": {"application/x-ndjson"}},
	}
	set := func(b *inspector.Body, data []byte, total int64, trunc bool) {
		*b = inspector.Body{Data: data, Total: total, Truncated: trunc}
	}
	set(&r.Client.ReqBody, []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"héllo ✓"}]}`), 70, false)
	set(&r.Client.RespBody, []byte(`{"id":"c1","usage":{"prompt_tokens":12}}`), 40, false)
	set(&r.Upstream.ReqBody, []byte(`{"model":"llama3"}`), 18, false)
	set(&r.Upstream.RespBody, []byte{0xff, 0xfe, 0x00, 0x01}, 4096, true) // binary + truncated
	return r
}

func open(t *testing.T, path string, o Options) *Store {
	t.Helper()
	o.Path = path
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func verifyDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRoundTripFullFidelity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "t.db")
	s := open(t, path, Options{})
	s.Archive(sample(time.Now()))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Saved() != 1 {
		t.Fatalf("saved=%d", s.Saved())
	}

	db := verifyDB(t, path)
	var (
		reqH, upH, path2, model string
		status, stream, up      int
		dur, fb, resSize        int64
		trunc                   int
		resBodyType, upResType  string
		extracted               string
	)
	err := db.QueryRow(`SELECT req_headers, up_req_headers, path, model, status, stream, up_status, duration_ms, first_byte_ms,
	                           res_body_size, up_res_body_truncated, typeof(res_body), typeof(up_res_body),
	                           json_extract(req_body, '$.messages[0].content')
	                    FROM exchanges`).Scan(&reqH, &upH, &path2, &model, &status, &stream, &up, &dur, &fb, &resSize, &trunc, &resBodyType, &upResType, &extracted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reqH, "Bearer client-secret-key-123456") || !strings.Contains(upH, "upstream-secret-key-7890") {
		t.Errorf("headers must be stored in full: %s | %s", reqH, upH)
	}
	if !strings.Contains(upH, `"X-Multi":["a","b"]`) {
		t.Errorf("multi-value headers lost: %s", upH)
	}
	if path2 != "/v1/chat/completions?x=1" || model != "gpt-4o" || status != 200 || stream != 1 || up != 200 || dur != 1500 || fb != 200 {
		t.Errorf("bad scalar columns: %s %s %d %d %d %d %d", path2, model, status, stream, up, dur, fb)
	}
	if extracted != "héllo ✓" {
		t.Errorf("json_extract on stored body failed: %q", extracted)
	}
	if resBodyType != "text" || upResType != "blob" {
		t.Errorf("storage classes: res=%s up_res=%s", resBodyType, upResType)
	}
	if trunc != 1 {
		t.Errorf("truncated flag not set")
	}
	var n, tot int
	if err := db.QueryRow(`SELECT count(*), sum(total_tokens) FROM exchange_summary`).Scan(&n, &tot); err != nil || n != 1 || tot != 46 {
		t.Errorf("summary view: n=%d tot=%d err=%v", n, tot, err)
	}
	var v int
	_ = db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != schemaVersion {
		t.Errorf("user_version=%d", v)
	}
}

func TestRedactOption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	s := open(t, path, Options{Redact: true})
	s.Archive(sample(time.Now()))
	_ = s.Close()
	var h string
	if err := verifyDB(t, path).QueryRow(`SELECT up_req_headers FROM exchanges`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, "upstream-secret-key-7890") || !strings.Contains(h, "••") {
		t.Errorf("secret not masked: %s", h)
	}
}

func TestReopenAppendsAndRetentionPrunes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	s := open(t, path, Options{})
	s.Archive(sample(time.Now().Add(-72 * time.Hour)))
	s.Archive(sample(time.Now()))
	_ = s.Close()

	s = open(t, path, Options{Retention: 24 * time.Hour})
	_ = s.Close() // prune runs at start-up
	var n int
	if err := verifyDB(t, path).QueryRow(`SELECT count(*) FROM exchanges`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected only the recent row to survive, n=%d err=%v", n, err)
	}
}

func TestArchiveNeverBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	s := open(t, path, Options{QueueSize: 1})
	const total = 500
	start := time.Now()
	for i := 0; i < total; i++ {
		s.Archive(sample(time.Now()))
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Archive blocked the caller")
	}
	_ = s.Close()
	if s.Saved()+s.Dropped() != total {
		t.Errorf("saved=%d dropped=%d, want sum %d", s.Saved(), s.Dropped(), total)
	}
	s.Archive(sample(time.Now())) // after Close: must be a harmless no-op
}
