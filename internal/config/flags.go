package config

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// Options are the raw command-line options.
type Options struct {
	ConfigPath     string
	Listen         string
	UpstreamURL    string
	UpstreamType   string
	UpstreamKey    string
	HostNames      multi
	Dialects       string
	APIKeys        multi
	SetHeaders     multi
	RemoveHeaders  multi
	ForwardHeaders multi
	ModelMap       multi
	SQLitePath     string
	SQLiteRedact   bool
	TUI            bool
	LogLevel       string
	Version        bool
}

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

const usageText = `LLMux — an LLM gateway that speaks OpenAI and Ollama.

Usage:
  llmux [flags]

Examples:
  llmux --config llmux.yaml
  llmux --upstream http://localhost:11434 --upstream-type ollama --listen :8080
  llmux --upstream https://api.openai.com --upstream-key $OPENAI_API_KEY --api-key secret --tui
  llmux --upstream http://localhost:11434 --upstream-type ollama --sqlite traffic.db

Flags:
`

// ParseFlags parses args (without the program name).
func ParseFlags(args []string, out io.Writer) (*Options, error) {
	o := &Options{}
	fs := flag.NewFlagSet("llmux", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprint(out, usageText); fs.PrintDefaults() }

	fs.StringVar(&o.ConfigPath, "config", "", "path to YAML configuration file")
	fs.StringVar(&o.ConfigPath, "c", "", "shorthand for --config")
	fs.StringVar(&o.Listen, "listen", "", "listen address, e.g. :8080 or 127.0.0.1:9000")
	fs.StringVar(&o.Listen, "l", "", "shorthand for --listen")
	fs.StringVar(&o.UpstreamURL, "upstream", "", "upstream server root URL; creates a catch-all virtual host")
	fs.StringVar(&o.UpstreamType, "upstream-type", "", "upstream API type: openai | ollama (default openai)")
	fs.StringVar(&o.UpstreamKey, "upstream-key", "", "API key LLMux sends to the upstream")
	fs.Var(&o.HostNames, "host", "DNS name served by the CLI virtual host (repeatable, default *)")
	fs.StringVar(&o.Dialects, "dialects", "", "comma-separated dialects exposed by the CLI host (default: all)")
	fs.Var(&o.APIKeys, "api-key", "client API key accepted by the gateway (repeatable)")
	fs.Var(&o.SetHeaders, "set-header", `override an upstream request header, "Name: value" (repeatable)`)
	fs.Var(&o.RemoveHeaders, "remove-header", "drop an upstream request header (repeatable)")
	fs.Var(&o.ForwardHeaders, "forward-header", "only forward these client headers upstream (repeatable)")
	fs.Var(&o.ModelMap, "model-map", `rewrite model names, "alias=real" (repeatable)`)
	fs.StringVar(&o.SQLitePath, "sqlite", "", "persist every request/response (full headers and bodies) into this SQLite file")
	fs.BoolVar(&o.SQLiteRedact, "sqlite-redact", false, "mask credentials in headers stored in SQLite")
	fs.BoolVar(&o.TUI, "tui", false, "show the interactive terminal UI")
	fs.StringVar(&o.LogLevel, "log-level", "", "debug | info | warn | error")
	fs.BoolVar(&o.Version, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return o, nil
}

// Resolve builds the final configuration: defaults < YAML file < flags.
func Resolve(o *Options) (Config, error) {
	cfg := Default()
	if o.ConfigPath != "" {
		var err error
		if cfg, err = Load(o.ConfigPath); err != nil {
			return cfg, err
		}
	}
	if o.Listen != "" {
		cfg.Server.Listen = o.Listen
	}
	if o.LogLevel != "" {
		cfg.Server.LogLevel = o.LogLevel
	}
	if o.TUI {
		cfg.TUI = true
	}
	if o.SQLitePath != "" {
		cfg.Storage.SQLite.Enabled = true
		cfg.Storage.SQLite.Path = o.SQLitePath
	}
	if o.SQLiteRedact {
		cfg.Storage.SQLite.Redact = true
	}

	if o.UpstreamURL != "" {
		h := Host{
			Name:     "cli",
			Hosts:    o.HostNames,
			APIKeys:  o.APIKeys,
			Upstream: Upstream{Type: o.UpstreamType, URL: o.UpstreamURL, APIKey: o.UpstreamKey},
		}
		if o.Dialects != "" {
			for _, d := range strings.Split(o.Dialects, ",") {
				if d = strings.TrimSpace(d); d != "" {
					h.Dialects = append(h.Dialects, d)
				}
			}
		}
		h.Headers.Request.Remove = o.RemoveHeaders
		h.Headers.Request.Forward = o.ForwardHeaders
		if len(o.SetHeaders) > 0 {
			h.Headers.Request.Set = map[string]string{}
			for _, s := range o.SetHeaders {
				k, v, ok := strings.Cut(s, ":")
				if !ok || strings.TrimSpace(k) == "" {
					return cfg, fmt.Errorf(`--set-header %q: expected "Name: value"`, s)
				}
				h.Headers.Request.Set[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if len(o.ModelMap) > 0 {
			h.ModelMap = map[string]string{}
			for _, s := range o.ModelMap {
				k, v, ok := strings.Cut(s, "=")
				if !ok || k == "" {
					return cfg, fmt.Errorf(`--model-map %q: expected "alias=real"`, s)
				}
				h.ModelMap[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		cfg.Hosts = append(cfg.Hosts, h)
	} else if len(o.APIKeys) > 0 {
		cfg.Auth.APIKeys = append(cfg.Auth.APIKeys, o.APIKeys...)
	}
	if err := cfg.Normalize(); err != nil {
		return cfg, err
	}
	return cfg, nil
}
