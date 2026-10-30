package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndNormalize(t *testing.T) {
	t.Setenv("MY_KEY", "sk-123")
	path := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(path, []byte(`
server: {listen: ":9000"}
auth: {api_keys: ["${MY_KEY}", "${MISSING:-fallback}"]}
hosts:
  - name: a
    upstream: {type: Ollama, url: "http://x:11434"}
`), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	h := cfg.Hosts[0]
	if cfg.Server.Listen != ":9000" || h.Upstream.Type != "ollama" || h.Hosts[0] != "*" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if len(h.APIKeys) != 2 || h.APIKeys[0] != "sk-123" || h.APIKeys[1] != "fallback" {
		t.Errorf("env expansion / key inheritance failed: %v", h.APIKeys)
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(path, []byte("sever: {}\n"), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("typo in key must be an error")
	}
}

func TestFlagsBuildCatchAllHost(t *testing.T) {
	o, err := ParseFlags([]string{
		"--upstream", "http://localhost:11434", "--upstream-type", "ollama",
		"--api-key", "k", "--set-header", "X-A: b", "--remove-header", "Cookie",
		"--model-map", "gpt=llama3", "--listen", ":1234", "--tui",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(o)
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Hosts[0]
	if !cfg.TUI || cfg.Server.Listen != ":1234" || h.Upstream.Type != "ollama" ||
		h.Headers.Request.Set["X-A"] != "b" || h.ModelMap["gpt"] != "llama3" || h.APIKeys[0] != "k" {
		t.Errorf("bad resolution: %+v", cfg)
	}
}

func TestNoHostsIsAnError(t *testing.T) {
	o, _ := ParseFlags(nil, io.Discard)
	if _, err := Resolve(o); err == nil {
		t.Fatal("expected error when nothing is configured")
	}
}

func TestParseByteSize(t *testing.T) {
	ok := map[string]ByteSize{
		"0": 0, "36": 36, "36b": 36, "36B": 36,
		"36k": 36 << 10, "36K": 36 << 10,
		"36m": 36 << 20, "36M": 36 << 20,
		"36g": 36 << 30, "36G": 36 << 30,
		" 64m ": 64 << 20, "007k": 7 << 10,
	}
	for in, want := range ok {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "k", "-1", "1.5m", "1 m", "1mb", "1kb", "36t", "abc", "0x10",
		"9223372036854775807k", "99999999999999999999"} {
		if v, err := ParseByteSize(in); err == nil {
			t.Errorf("ParseByteSize(%q) = %d, expected an error", in, v)
		}
	}
}

func TestByteSizeString(t *testing.T) {
	for in, want := range map[ByteSize]string{0: "0b", 36: "36b", 1024: "1k", 36 << 20: "36m", 3 << 30: "3g", 1500: "1500b"} {
		if got := in.String(); got != want {
			t.Errorf("%d -> %q, want %q", in, got, want)
		}
	}
}

func TestSizesInYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	t.Setenv("LIMIT", "2m")
	_ = os.WriteFile(path, []byte(`
server: {max_body_bytes: 8m}
inspector: {body_limit: 512k}
storage: {sqlite: {enabled: true, body_limit: "${LIMIT}"}}
hosts: [{upstream: {url: "http://x"}}]
`), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.MaxBodyBytes != 8<<20 || cfg.Inspector.BodyLimit != 512<<10 || cfg.Storage.SQLite.BodyLimit != 2<<20 {
		t.Errorf("got %v %v %v", cfg.Server.MaxBodyBytes, cfg.Inspector.BodyLimit, cfg.Storage.SQLite.BodyLimit)
	}

	// plain integers (bytes) keep working
	_ = os.WriteFile(path, []byte("server: {max_body_bytes: 33554432}\ninspector: {body_limit: 100}\nhosts: [{upstream: {url: \"http://x\"}}]\n"), 0o600)
	cfg, err = Load(path)
	if err != nil || cfg.Server.MaxBodyBytes != 32<<20 || cfg.Inspector.BodyLimit != 100 {
		t.Fatalf("plain ints: %v %+v", err, cfg.Server)
	}
}

func TestBadSizeReportsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(path, []byte("server:\n  max_body_bytes: 12mb\n"), 0o600)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "12mb") {
		t.Fatalf("want a clear error with the line number, got: %v", err)
	}
	_ = os.WriteFile(path, []byte("server:\n  max_body_bytes: [1]\n"), 0o600)
	if _, err = Load(path); err == nil {
		t.Fatal("a list is not a size")
	}
}

func TestZeroMaxBodyRejected(t *testing.T) {
	cfg := Default()
	cfg.Server.MaxBodyBytes = 0
	cfg.Hosts = []Host{{Upstream: Upstream{URL: "http://x"}}}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("max_body_bytes: 0 would reject every request and must be an error")
	}
}
