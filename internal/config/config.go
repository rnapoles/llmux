// Package config loads, validates and merges LLMux configuration coming from
// a YAML file and/or command-line flags.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    Server    `yaml:"server"`
	Auth      Auth      `yaml:"auth"`
	Inspector Inspector `yaml:"inspector"`
	TUI       bool      `yaml:"tui"`
	Hosts     []Host    `yaml:"hosts"`
}

type Server struct {
	Listen            string        `yaml:"listen"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	MaxBodyBytes      int64         `yaml:"max_body_bytes"`
	LogLevel          string        `yaml:"log_level"` // debug|info|warn|error
	LogFile           string        `yaml:"log_file"`  // used when the TUI owns the terminal
}

// Auth holds gateway-wide client API keys, used by hosts that define none.
type Auth struct {
	APIKeys []string `yaml:"api_keys"`
}

// Inspector configures traffic recording (always on with --tui).
type Inspector struct {
	Enabled   bool `yaml:"enabled"`
	History   int  `yaml:"history"`    // max exchanges kept in memory
	BodyLimit int  `yaml:"body_limit"` // max captured bytes per body
	Redact    bool `yaml:"redact"`     // mask credentials in captured headers
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

// Default returns the configuration used before files/flags are applied.
func Default() Config {
	return Config{
		Server: Server{
			Listen:            ":8080",
			ReadHeaderTimeout: 15 * time.Second,
			IdleTimeout:       120 * time.Second,
			ShutdownTimeout:   10 * time.Second,
			MaxBodyBytes:      32 << 20,
			LogLevel:          "info",
		},
		Inspector: Inspector{History: 1000, BodyLimit: 1 << 20, Redact: true},
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
	if len(c.Hosts) == 0 {
		return errors.New("no virtual hosts configured: provide --config or --upstream")
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
