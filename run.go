package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/sanketsudake/claude-agent-proxy/internal/config"
	"github.com/sanketsudake/claude-agent-proxy/internal/proxy"
)

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler, err := proxy.New(proxy.Options{
		UpstreamURL:     cfg.UpstreamURL,
		MaxRequestBytes: cfg.MaxRequestBytes,
		MaxCaptureBytes: cfg.MaxCaptureBytes,
		OnCapture:       nil, // capture pipeline wired in Phase 2
		Logger:          logger,
	})
	if err != nil {
		return err
	}

	return proxy.Serve(ctx, fmt.Sprintf(":%d", cfg.Port), handler, logger)
}
