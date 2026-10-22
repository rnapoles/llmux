package config

import (
	"io"
	"os"
	"path/filepath"
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
