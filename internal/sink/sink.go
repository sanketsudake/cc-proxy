// Package sink defines the pluggable capture-consumer interface and the
// fan-out dispatcher that keeps every sink off the proxy's hot path.
package sink

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/config"
)

// Sink consumes capture records. Write is called from a single dedicated
// goroutine per sink, so implementations need no internal locking. Close
// flushes any buffered state.
type Sink interface {
	Name() string
	Write(ctx context.Context, rec *capture.Record) error
	Close(ctx context.Context) error
}

// Shutdown/write budgets. Deliberate ordering: a single Write may take up to
// writeTimeout, which exceeds the shutdown drain window in run.go — on exit
// we prefer leaving over waiting out one hung remote call, because records
// are drop-tolerant by design.
const (
	writeTimeout      = 60 * time.Second
	closeFlushTimeout = 15 * time.Second
)

type worker struct {
	sink  Sink
	queue chan *capture.Record
	done  chan struct{}
}

// Dispatcher fans records out to sinks, one buffered queue + goroutine per
// sink so a hung sink never stalls the others. Dispatch must be called from
// a single goroutine (the builder stage in run.go).
type Dispatcher struct {
	workers  []*worker
	policy   string // config.QueuePolicyDrop or config.QueuePolicyBlock
	logger   *slog.Logger
	mu       sync.Mutex
	lastWarn map[string]time.Time
}

// NewDispatcher starts one worker per sink.
func NewDispatcher(logger *slog.Logger, queueSize int, policy string, sinks ...Sink) *Dispatcher {
	d := &Dispatcher{policy: policy, logger: logger, lastWarn: map[string]time.Time{}}
	for _, s := range sinks {
		w := &worker{sink: s, queue: make(chan *capture.Record, queueSize), done: make(chan struct{})}
		d.workers = append(d.workers, w)
		go d.run(w)
	}
	return d
}

func (d *Dispatcher) run(w *worker) {
	defer close(w.done)
	for rec := range w.queue {
		d.write(w, rec)
	}
	// Drained; let the sink flush.
	ctx, cancel := context.WithTimeout(context.Background(), closeFlushTimeout)
	if err := w.sink.Close(ctx); err != nil {
		d.logger.Warn("sink close failed", "sink", w.sink.Name(), "err", err)
	}
	cancel()
}

func (d *Dispatcher) write(w *worker, rec *capture.Record) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("sink panicked", "sink", w.sink.Name(), "panic", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := w.sink.Write(ctx, rec); err != nil {
		d.warnRateLimited(w.sink.Name(), "sink write failed", err)
	}
}

// Dispatch fans one record out to every sink. Non-blocking under the drop
// policy: a full queue drops the record for that sink with a warning.
func (d *Dispatcher) Dispatch(rec *capture.Record) {
	for _, w := range d.workers {
		if d.policy == config.QueuePolicyBlock {
			w.queue <- rec
			continue
		}
		select {
		case w.queue <- rec:
		default:
			d.warnRateLimited(w.sink.Name(), "queue full, record dropped", nil)
		}
	}
}

// Close stops accepting records, drains queues, and flushes sinks.
func (d *Dispatcher) Close(ctx context.Context) error {
	for _, w := range d.workers {
		close(w.queue)
	}
	for _, w := range d.workers {
		select {
		case <-w.done:
		case <-ctx.Done():
			d.logger.Warn("sink drain timed out", "sink", w.sink.Name())
		}
	}
	return nil
}

// warnRateLimited logs at most once per minute per sink so a down backend
// doesn't flood the terminal.
func (d *Dispatcher) warnRateLimited(sink, msg string, err error) {
	d.mu.Lock()
	last := d.lastWarn[sink]
	now := time.Now()
	if now.Sub(last) < time.Minute {
		d.mu.Unlock()
		return
	}
	d.lastWarn[sink] = now
	d.mu.Unlock()
	if err != nil {
		d.logger.Warn(msg, "sink", sink, "err", err)
	} else {
		d.logger.Warn(msg, "sink", sink)
	}
}
