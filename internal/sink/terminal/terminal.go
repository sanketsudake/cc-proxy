// Package terminal prints the compact per-request audit table — the live
// view of what is eating your context.
package terminal

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/sanketsudake/claude-agent-proxy/internal/capture"
)

const topN = 12

type Sink struct {
	Out io.Writer
}

func New(out io.Writer) *Sink { return &Sink{Out: out} }

func (s *Sink) Name() string { return "terminal" }

func (s *Sink) Write(_ context.Context, rec *capture.Record) error {
	fmt.Fprintf(s.Out, "\n[claude-agent-proxy] %s %s · status %d · %d tools · %s tool bytes\n",
		rec.Model, rec.Endpoint, rec.StatusCode, len(rec.Tools), formatInt(rec.ToolsBytes))

	tw := tabwriter.NewWriter(s.Out, 2, 4, 2, ' ', 0)
	shown := rec.Tools
	if len(shown) > topN {
		shown = shown[:topN]
	}
	for _, t := range shown {
		fmt.Fprintf(tw, "  %s\t%s B\t~%s tok\t%s%%\n",
			t.Name, formatInt(t.Bytes), formatInt(t.ApproxTokens), pct(t.Bytes, rec.TotalBytes))
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
		formatInt(rec.Usage.InputTokens), formatInt(rec.Usage.OutputTokens),
		formatInt(rec.Usage.CacheReadTokens), formatInt(rec.Usage.CacheCreationTokens),
		cost,
		time.Duration(rec.TTFTMS)*time.Millisecond,
		time.Duration(rec.LatencyMS)*time.Millisecond)
	if rec.ResponseError != "" {
		fmt.Fprintf(s.Out, "  response error: %s\n", rec.ResponseError)
	}
	return nil
}

func (s *Sink) Close(context.Context) error { return nil }

func pct(part, total int) string {
	if total == 0 {
		return "0.0"
	}
	return fmt.Sprintf("%.1f", float64(part)/float64(total)*100)
}

// formatInt renders n with thousands separators (1234567 -> "1,234,567").
func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
