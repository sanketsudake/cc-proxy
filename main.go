// claude-agent-proxy — see what Claude Code actually sends the model.
//
// A transparent logging proxy between Claude Code and the Anthropic API.
// Point Claude Code at it:
//
//	ANTHROPIC_BASE_URL=http://localhost:8787 claude
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/sanketsudake/claude-agent-proxy/internal/config"
	"github.com/sanketsudake/claude-agent-proxy/internal/version"
)

func main() {
	cfg, err := config.Load(os.Args[1:], os.Stderr)
	if errors.Is(err, config.ErrVersionRequested) {
		fmt.Println("claude-agent-proxy", version.String())
		return
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	logger := newLogger(cfg)
	if err := run(cfg, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.SlogLevel()}
	var h slog.Handler
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}
