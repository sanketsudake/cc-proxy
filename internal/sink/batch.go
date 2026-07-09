package sink

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Batcher accumulates records and flushes them by size or age. Remote sinks
// (ClickHouse, Loki) embed it so inserts amortize over batches and transient
// backend failures retry with backoff instead of dropping single records.
//
// Write is only ever called from the sink's dispatcher worker goroutine; the
// mutex exists for the background age-based flusher.
type Batcher[T any] struct {
	mu       sync.Mutex
	buf      []T
	size     int
	interval time.Duration
	flush    func(ctx context.Context, batch []T) error
	logger   *slog.Logger
	name     string
	stop     chan struct{}
	stopOnce sync.Once
}

// NewBatcher starts the age-based background flusher. size and interval must
// be positive — config.Load defaults them; sinks pass config values through.
func NewBatcher[T any](name string, size int, interval time.Duration, logger *slog.Logger, flush func(context.Context, []T) error) *Batcher[T] {
	b := &Batcher[T]{size: size, interval: interval, flush: flush, logger: logger, name: name, stop: make(chan struct{})}
	go b.loop()
	return b
}

func (b *Batcher[T]) loop() {
	t := time.NewTicker(b.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.Flush(context.Background())
		case <-b.stop:
			return
		}
	}
}

// Add buffers one item, flushing when the batch is full.
func (b *Batcher[T]) Add(ctx context.Context, item T) {
	b.mu.Lock()
	b.buf = append(b.buf, item)
	full := len(b.buf) >= b.size
	b.mu.Unlock()
	if full {
		b.Flush(ctx)
	}
}

// Flush sends the buffered batch with exponential-backoff retries. A batch
// that still fails after the retries is dropped with a warning — capture
// loss is preferred over unbounded memory growth when a backend is down.
func (b *Batcher[T]) Flush(ctx context.Context) {
	b.mu.Lock()
	batch := b.buf
	b.buf = nil
	b.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	backoff := 500 * time.Millisecond
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				b.logger.Warn("batch flush cancelled", "sink", b.name, "dropped", len(batch))
				return
			}
			backoff *= 4
		}
		if err = b.flush(ctx, batch); err == nil {
			return
		}
	}
	b.logger.Warn("batch flush failed, dropping batch", "sink", b.name, "dropped", len(batch), "err", err)
}

// Close stops the background flusher and flushes what remains.
func (b *Batcher[T]) Close(ctx context.Context) {
	b.stopOnce.Do(func() { close(b.stop) })
	b.Flush(ctx)
}
