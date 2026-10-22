// Command llmux is an LLM gateway emulating the OpenAI and Ollama APIs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/rnapoles/llmux/internal/app"
	"github.com/rnapoles/llmux/internal/config"
)

// Set at build time: -ldflags "-X main.version=1.0.0"
var version = "dev"

func main() {
	opts, err := config.ParseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if opts.Version {
		fmt.Println("llmux", version)
		return
	}
	cfg, err := config.Resolve(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llmux:", err)
		os.Exit(2)
	}
	if err := app.Run(cfg, version); err != nil {
		fmt.Fprintln(os.Stderr, "llmux:", err)
		os.Exit(1)
	}
}
