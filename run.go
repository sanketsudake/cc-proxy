package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sanketsudake/claude-agent-proxy/internal/capture"
	"github.com/sanketsudake/claude-agent-proxy/internal/config"
	"github.com/sanketsudake/claude-agent-proxy/internal/cost"
	"github.com/sanketsudake/claude-agent-proxy/internal/proxy"
	"github.com/sanketsudake/claude-agent-proxy/internal/sink"
	"github.com/sanketsudake/claude-agent-proxy/internal/sink/clickhouse"
	"github.com/sanketsudake/claude-agent-proxy/internal/sink/loki"
	"github.com/sanketsudake/claude-agent-proxy/internal/sink/markdown"
	"github.com/sanketsudake/claude-agent-proxy/internal/sink/sqlite"
	"github.com/sanketsudake/claude-agent-proxy/internal/sink/terminal"
)

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sinks, err := buildSinks(cfg, logger)
	if err != nil {
		return err
	}
	dispatcher := sink.NewDispatcher(logger, cfg.QueueSize, sink.Policy(cfg.QueuePolicy), sinks...)
	builder := &capture.Builder{Estimator: cost.NewEstimator(cfg.Pricing)}

	handler, err := proxy.New(proxy.Options{
		UpstreamURL:     cfg.UpstreamURL,
		MaxRequestBytes: cfg.MaxRequestBytes,
		MaxCaptureBytes: cfg.MaxCaptureBytes,
		OnCapture: func(c *proxy.Capture) {
			// Runs on the response-body goroutine: build off it entirely.
			go func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("capture pipeline panicked", "panic", r)
					}
				}()
				dispatcher.Dispatch(builder.Build(c))
			}()
		},
		Logger: logger,
	})
	if err != nil {
		return err
	}

	serveErr := proxy.Serve(ctx, fmt.Sprintf(":%d", cfg.Port), handler, logger)

	drainCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := dispatcher.Close(drainCtx); err != nil {
		logger.Warn("dispatcher close", "err", err)
	}
	return serveErr
}

func buildSinks(cfg config.Config, logger *slog.Logger) ([]sink.Sink, error) {
	var sinks []sink.Sink
	if !cfg.Quiet {
		sinks = append(sinks, terminal.New(os.Stdout))
	}
	if cfg.Sinks.Markdown.Enabled {
		sinks = append(sinks, markdown.New(cfg.Sinks.Markdown.Dir))
		logger.Info("markdown sink enabled", "dir", cfg.Sinks.Markdown.Dir)
	}
	if cfg.Sinks.SQLite.Enabled {
		s, err := sqlite.New(cfg.Sinks.SQLite.Path)
		if err != nil {
			return nil, fmt.Errorf("sqlite sink: %w", err)
		}
		sinks = append(sinks, s)
		logger.Info("sqlite sink enabled", "path", cfg.Sinks.SQLite.Path)
	}
	if cfg.Sinks.ClickHouse.Enabled {
		sinks = append(sinks, clickhouse.New(cfg.Sinks.ClickHouse, logger))
		logger.Info("clickhouse sink enabled", "url", cfg.Sinks.ClickHouse.URL)
	}
	if cfg.Sinks.Loki.Enabled {
		sinks = append(sinks, loki.New(cfg.Sinks.Loki, logger))
		logger.Info("loki sink enabled", "url", cfg.Sinks.Loki.URL)
	}
	return sinks, nil
}
