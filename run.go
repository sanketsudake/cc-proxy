package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/config"
	"github.com/sanketsudake/cc-proxy/internal/cost"
	"github.com/sanketsudake/cc-proxy/internal/proxy"
	"github.com/sanketsudake/cc-proxy/internal/sink"
	"github.com/sanketsudake/cc-proxy/internal/sink/clickhouse"
	"github.com/sanketsudake/cc-proxy/internal/sink/loki"
	"github.com/sanketsudake/cc-proxy/internal/sink/markdown"
	"github.com/sanketsudake/cc-proxy/internal/sink/sqlite"
	"github.com/sanketsudake/cc-proxy/internal/sink/terminal"
)

// drainTimeout bounds shutdown: the HTTP server drains first (30s inside
// proxy.Serve), then sinks get this long to flush. It is deliberately
// shorter than the per-sink write timeout — on exit we abandon a hung
// remote write rather than wait it out (records are drop-tolerant).
const drainTimeout = 20 * time.Second

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sinks, err := buildSinks(cfg, logger)
	if err != nil {
		return err
	}
	dispatcher := sink.NewDispatcher(logger, cfg.QueueSize, cfg.QueuePolicy, sinks...)
	pipeline := newPipeline(cfg, dispatcher, logger)

	handler, err := proxy.New(proxy.Options{
		UpstreamURL:     cfg.UpstreamURL,
		MaxRequestBytes: cfg.MaxRequestBytes,
		MaxCaptureBytes: cfg.MaxCaptureBytes,
		OnCapture:       pipeline.enqueue,
		Logger:          logger,
	})
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(proxy.HealthzPath, proxy.Healthz)
	mux.Handle("/", handler)

	serveErr := proxy.Serve(ctx, fmt.Sprintf(":%d", cfg.Port), mux, logger)

	pipeline.close()
	drainCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if err := dispatcher.Close(drainCtx); err != nil {
		logger.Warn("dispatcher close", "err", err)
	}
	return serveErr
}

// pipeline is the single builder stage between the proxy and the sinks: raw
// captures go through one bounded queue and one goroutine that parses them
// (the expensive step) and fans the record out. This bounds concurrency at
// the widest point of the pipeline and keeps Dispatch single-caller, which
// the Dispatcher relies on.
type pipeline struct {
	queue  chan *proxy.Capture
	done   chan struct{}
	logger *slog.Logger
	block  bool
}

func newPipeline(cfg config.Config, dispatcher *sink.Dispatcher, logger *slog.Logger) *pipeline {
	p := &pipeline{
		queue:  make(chan *proxy.Capture, cfg.QueueSize),
		done:   make(chan struct{}),
		logger: logger,
		block:  cfg.QueuePolicy == config.QueuePolicyBlock,
	}
	builder := &capture.Builder{Estimator: cost.NewEstimator(cfg.Pricing)}
	go func() {
		defer close(p.done)
		for c := range p.queue {
			p.build(builder, dispatcher, c)
		}
	}()
	return p
}

func (p *pipeline) build(builder *capture.Builder, dispatcher *sink.Dispatcher, c *proxy.Capture) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("capture pipeline panicked", "panic", r)
		}
	}()
	dispatcher.Dispatch(builder.Build(c))
}

// enqueue is the proxy's OnCapture callback; it must not block the response
// goroutine, so under the drop policy a full queue loses the capture.
func (p *pipeline) enqueue(c *proxy.Capture) {
	if p.block {
		p.queue <- c
		return
	}
	select {
	case p.queue <- c:
	default:
		p.logger.Warn("capture queue full, exchange not recorded", "path", c.Path)
	}
}

func (p *pipeline) close() {
	close(p.queue)
	<-p.done
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
