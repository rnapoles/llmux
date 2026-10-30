// Package config loads, validates and merges LLMux configuration coming from
// a YAML file and/or command-line flags.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    Server    `yaml:"server"`
	Auth      Auth      `yaml:"auth"`
	Inspector Inspector `yaml:"inspector"`
	Storage   Storage   `yaml:"storage"`
	TUI       bool      `yaml:"tui"`
	Hosts     []Host    `yaml:"hosts"`
}

type Server struct {
	Listen            string        `yaml:"listen"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	MaxBodyBytes      ByteSize      `yaml:"max_body_bytes"` // e.g. 32m
	LogLevel          string        `yaml:"log_level"`      // debug|info|warn|error
	LogFile           string        `yaml:"log_file"`       // used when the TUI owns the terminal
}

// Auth holds gateway-wide client API keys, used by hosts that define none.
type Auth struct {
	APIKeys []string `yaml:"api_keys"`
}

// Inspector configures traffic recording (always on with --tui).
type Inspector struct {
	Enabled   bool     `yaml:"enabled"`
	History   int      `yaml:"history"`    // max exchanges kept in memory
	BodyLimit ByteSize `yaml:"body_limit"` // max captured size per body, e.g. 1m
	Redact    bool     `yaml:"redact"`     // mask credentials when *displaying* headers in the TUI
}

// Storage configures optional persistence of every exchange.
type Storage struct {
	SQLite SQLite `yaml:"sqlite"`
}

// SQLite persists each request/response (both legs, full headers and bodies)
// into a SQLite database for later analysis.
type SQLite struct {
	Enabled   bool          `yaml:"enabled"`
	Path      string        `yaml:"path"`       // database file
	BodyLimit ByteSize      `yaml:"body_limit"` // max size stored per body, e.g. 64m (0 = default)
	Redact    bool          `yaml:"redact"`     // mask credentials in stored headers (default: store them in full)
	Retention time.Duration `yaml:"retention"`  // delete rows older than this (0 = keep forever)
	QueueSize int           `yaml:"queue_size"` // buffered exchanges awaiting write; excess is dropped, never blocks requests
}

// Host is a virtual host: a set of DNS names served with its own dialects,
// credentials, upstream and header rules.
type Host struct {
	Name     string            `yaml:"name"`
	Hosts    []string          `yaml:"hosts"`    // "api.example.com", "*.example.com" or "*"
	Dialects []string          `yaml:"dialects"` // empty = every registered dialect
	APIKeys  []string          `yaml:"api_keys"`
	Upstream Upstream          `yaml:"upstream"`
	ModelMap map[string]string `yaml:"model_map"`
	Headers  Headers           `yaml:"headers"`
}

type Upstream struct {
	Type               string        `yaml:"type"` // openai | ollama | ...
	URL                string        `yaml:"url"`
	APIKey             string        `yaml:"api_key"`
	Timeout            time.Duration `yaml:"timeout"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
}

type Headers struct {
	Request  HeaderRules `yaml:"request"`
	Response HeaderRules `yaml:"response"`
}

// HeaderRules describe how headers are transformed.
//
//	Forward: allow-list of header names/patterns ("X-*"). Empty = forward all
//	         non-sensitive headers.
//	Remove:  names/patterns deleted after forwarding.
//	Set:     headers overridden/added (empty value deletes). Supports ${ENV}.
type HeaderRules struct {
	Forward []string          `yaml:"forward"`
	Remove  []string          `yaml:"remove"`
	Set     map[string]string `yaml:"set"`
}

// ByteSize is a size in bytes. In YAML it accepts a plain integer or a string
// matching  \d+(b|B|k|K|m|M|g|G)?  — no suffix means bytes, and the suffixes are
// binary multiples:  36 = 36b = 36 bytes · 36k = 36×1024 · 36m = 36×1024² · 36g = 36×1024³.
type ByteSize int64

var byteSizeRe = regexp.MustCompile(`^(\d+)([bBkKmMgG]?)$`)

// ParseByteSize parses strings such as "512", "64k", "32M" or "1g".
func ParseByteSize(s string) (ByteSize, error) {
	m := byteSizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("invalid size %q: expected digits with an optional b, k, m or g suffix (e.g. 512, 64k, 32m, 1g)", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: number too large", s)
	}
	mult := int64(1)
	switch strings.ToLower(m[2]) {
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return ByteSize(n * mult), nil
}

// UnmarshalYAML accepts both `max_body_bytes: 33554432` and `max_body_bytes: 32m`.
func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: size must be a number or a string like 32m", n.Line)
	}
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*b = v
	return nil
}

// Int64 returns the size in bytes.
func (b ByteSize) Int64() int64 { return int64(b) }

// Int returns the size as an int, saturating on platforms where int is 32 bits.
func (b ByteSize) Int() int {
	if int64(b) > int64(math.MaxInt) {
		return math.MaxInt
	}
	return int(b)
}

// String renders the largest exact unit, e.g. "32m", "1500b".
func (b ByteSize) String() string {
	switch {
	case b != 0 && b%(1<<30) == 0:
		return fmt.Sprintf("%dg", b>>30)
	case b != 0 && b%(1<<20) == 0:
		return fmt.Sprintf("%dm", b>>20)
	case b != 0 && b%(1<<10) == 0:
		return fmt.Sprintf("%dk", b>>10)
	}
	return fmt.Sprintf("%db", int64(b))
}

// Default returns the configuration used before files/flags are applied.
func Default() Config {
	return Config{
		Server: Server{
			Listen:            ":8080",
			ReadHeaderTimeout: 15 * time.Second,
			IdleTimeout:       120 * time.Second,
			ShutdownTimeout:   10 * time.Second,
			MaxBodyBytes:      32 << 20, // 32m
			LogLevel:          "info",
		},
		Inspector: Inspector{History: 1000, BodyLimit: 1 << 20, Redact: true},
		Storage:   Storage{SQLite: SQLite{Path: "llmux.db", BodyLimit: 64 << 20, QueueSize: 4096}},
	}
}

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandEnv replaces ${VAR} and ${VAR:-default} in raw YAML.
func expandEnv(b []byte) []byte {
	return envRe.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := envRe.FindSubmatch(m)
		if v, ok := os.LookupEnv(string(sub[1])); ok {
			return []byte(v)
		}
		return sub[2]
	})
}

// Load reads a YAML file on top of the defaults. Unknown keys are errors so
// typos do not go unnoticed.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(expandEnv(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Normalize fills per-host defaults and validates the result.
func (c *Config) Normalize() error {
	if c.Server.Listen == "" {
		return errors.New("server.listen must not be empty")
	}
	if c.Server.MaxBodyBytes <= 0 {
		return errors.New("server.max_body_bytes must be greater than zero (e.g. 32m)")
	}
	if len(c.Hosts) == 0 {
		return errors.New("no virtual hosts configured: provide --config or --upstream")
	}
	if sq := &c.Storage.SQLite; sq.Enabled {
		if strings.TrimSpace(sq.Path) == "" {
			sq.Path = "llmux.db"
		}
		if sq.BodyLimit <= 0 {
			sq.BodyLimit = 64 << 20
		}
		if sq.QueueSize <= 0 {
			sq.QueueSize = 4096
		}
		if sq.Retention < 0 {
			return errors.New("storage.sqlite.retention must not be negative")
		}
	}
	seen := map[string]bool{}
	for i := range c.Hosts {
		h := &c.Hosts[i]
		if h.Name == "" {
			h.Name = fmt.Sprintf("host-%d", i+1)
		}
		if seen[h.Name] {
			return fmt.Errorf("duplicate virtual host name %q", h.Name)
		}
		seen[h.Name] = true
		if len(h.Hosts) == 0 {
			h.Hosts = []string{"*"}
		}
		for j := range h.Hosts {
			h.Hosts[j] = strings.ToLower(strings.TrimSpace(h.Hosts[j]))
		}
		if h.Upstream.Type == "" {
			h.Upstream.Type = "openai"
		}
		h.Upstream.Type = strings.ToLower(h.Upstream.Type)
		if h.Upstream.Timeout == 0 {
			h.Upstream.Timeout = 5 * time.Minute
		}
		u, err := url.Parse(h.Upstream.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("host %q: upstream.url %q must be an absolute http(s) URL", h.Name, h.Upstream.URL)
		}
		if len(h.APIKeys) == 0 {
			h.APIKeys = c.Auth.APIKeys
		}
	}
	return nil
}
