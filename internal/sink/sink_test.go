package sink

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/capture"
)

type fakeSink struct {
	name    string
	delay   time.Duration
	written atomic.Int64
	closed  atomic.Bool
	err     error
}

func (f *fakeSink) Name() string { return f.name }
func (f *fakeSink) Write(context.Context, *capture.Record) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.written.Add(1)
	return f.err
}
func (f *fakeSink) Close(context.Context) error { f.closed.Store(true); return nil }

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A slow sink must not block Dispatch or starve fast sinks.
func TestSlowSinkDoesNotBlockOthers(t *testing.T) {
	slow := &fakeSink{name: "slow", delay: 200 * time.Millisecond}
	fast := &fakeSink{name: "fast"}
	d := NewDispatcher(discard(), 8, Drop, slow, fast)

	start := time.Now()
	for range 5 {
		d.Dispatch(&capture.Record{})
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("Dispatch blocked for %v", elapsed)
	}

	deadline := time.Now().Add(2 * time.Second)
	for fast.written.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fast.written.Load() != 5 {
		t.Errorf("fast sink wrote %d/5 while slow sink busy", fast.written.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.Close(ctx)
}

func TestDropPolicyOnFullQueue(t *testing.T) {
	slow := &fakeSink{name: "slow", delay: time.Second}
	d := NewDispatcher(discard(), 1, Drop, slow)
	for range 10 {
		d.Dispatch(&capture.Record{}) // must never block
	}
	if slow.dropped(d) == 0 {
		t.Error("expected drops with a full queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.Close(ctx)
}

func (f *fakeSink) dropped(d *Dispatcher) int64 {
	for _, w := range d.workers {
		if w.sink == f {
			return w.dropped
		}
	}
	return 0
}

func TestCloseDrainsAndFlushes(t *testing.T) {
	s := &fakeSink{name: "s", delay: 10 * time.Millisecond}
	d := NewDispatcher(discard(), 64, Drop, s)
	for range 10 {
		d.Dispatch(&capture.Record{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if s.written.Load() != 10 {
		t.Errorf("drained %d/10 before close", s.written.Load())
	}
	if !s.closed.Load() {
		t.Error("sink not flushed on close")
	}
}

func TestSinkErrorDoesNotPropagate(t *testing.T) {
	s := &fakeSink{name: "failing", err: errors.New("backend down")}
	d := NewDispatcher(discard(), 8, Drop, s)
	d.Dispatch(&capture.Record{}) // must not panic or block
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = d.Close(ctx)
	if s.written.Load() != 1 {
		t.Errorf("write attempts = %d", s.written.Load())
	}
}
