// Package terminal prints the compact per-request audit table — the live
// view of what is eating your context.
package terminal

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/capture"
)

const topN = 12

type Sink struct {
	Out io.Writer
}

func New(out io.Writer) *Sink { return &Sink{Out: out} }

func (s *Sink) Name() string { return "terminal" }

func (s *Sink) Write(_ context.Context, rec *capture.Record) error {
	session := ""
	if len(rec.SessionID) >= 8 {
		session = " · session " + rec.SessionID[:8]
	}
	retry := ""
	if rec.RetryCount > 0 {
		retry = fmt.Sprintf(" · RETRY %d", rec.RetryCount)
	}
	fmt.Fprintf(s.Out, "\n[cc-proxy] %s %s · status %d · %d tools · %s tool bytes%s%s\n",
		rec.Model, rec.Endpoint, rec.StatusCode, len(rec.Tools), audit.Comma(rec.ToolsBytes), session, retry)

	tw := tabwriter.NewWriter(s.Out, 2, 4, 2, ' ', 0)
	shown := rec.Tools
	if len(shown) > topN {
		shown = shown[:topN]
	}
	for _, t := range shown {
		fmt.Fprintf(tw, "  %s\t%s B\t~%s tok\t%.1f%%\n",
			t.Name, audit.Comma(t.Bytes), audit.Comma(t.ApproxTokens), audit.Pct(t.Bytes, rec.TotalBytes))
	}
	tw.Flush()
	if rest := len(rec.Tools) - topN; rest > 0 {
		fmt.Fprintf(s.Out, "  … %d more\n", rest)
	}

	cost := "unknown model"
	if rec.CostKnown {
		cost = fmt.Sprintf("~$%.4f", rec.CostUSD)
	}
	fmt.Fprintf(s.Out, "  in=%s out=%s cache_read=%s cache_write=%s · cost %s · ttft %s · total %s\n",
		audit.Comma(rec.Usage.InputTokens), audit.Comma(rec.Usage.OutputTokens),
		audit.Comma(rec.Usage.CacheReadTokens), audit.Comma(rec.Usage.CacheCreationTokens),
		cost,
		time.Duration(rec.TTFTMS)*time.Millisecond,
		time.Duration(rec.LatencyMS)*time.Millisecond)
	if rec.ResponseError != "" {
		fmt.Fprintf(s.Out, "  response error: %s\n", rec.ResponseError)
	}
	return nil
}

func (s *Sink) Close(context.Context) error { return nil }
