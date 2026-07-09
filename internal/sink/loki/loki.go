// Package loki pushes capture records as structured log lines to Grafana
// Loki via its HTTP push API. Lines carry the analytics fields plus a
// truncated response preview; raw payloads stay in markdown/SQLite.
package loki

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/config"
	"github.com/sanketsudake/cc-proxy/internal/sink"
)

const (
	pushPath = "/loki/api/v1/push"
	jobLabel = "cc-proxy" // the {job=...} stream label
)

type entry struct {
	ts    time.Time
	model string
	line  []byte
}

type Sink struct {
	batcher      *sink.Batcher[entry]
	client       *http.Client
	pushURL      string
	maxLineBytes int
}

func New(cfg config.LokiSink, logger *slog.Logger) *Sink {
	s := &Sink{
		client:       &http.Client{Timeout: 30 * time.Second},
		pushURL:      cfg.URL + pushPath,
		maxLineBytes: cfg.MaxLineBytes,
	}
	s.batcher = sink.NewBatcher("loki", cfg.BatchSize, cfg.FlushInterval.Std(), logger, s.send)
	return s
}

func (s *Sink) Name() string { return "loki" }

// line is the JSON log line pushed per request.
type line struct {
	ID           string  `json:"id"`
	Endpoint     string  `json:"endpoint"`
	Model        string  `json:"model"`
	SessionID    string  `json:"session_id,omitempty"`
	App          string  `json:"app,omitempty"`
	RetryCount   int     `json:"retry_count,omitempty"`
	AccountID    string  `json:"account_id,omitempty"`
	Status       int     `json:"status"`
	LatencyMS    int64   `json:"latency_ms"`
	TTFTMS       int64   `json:"ttft_ms"`
	StopReason   string  `json:"stop_reason,omitempty"`
	Error        string  `json:"error,omitempty"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CacheRead    int     `json:"cache_read_tokens"`
	CacheWrite   int     `json:"cache_creation_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	TotalBytes   int     `json:"total_bytes"`
	ToolCount    int     `json:"tool_count"`
	Response     string  `json:"response,omitempty"` // truncated text preview
}

func (s *Sink) Write(ctx context.Context, rec *capture.Record) error {
	l := line{
		ID:           rec.ID,
		Endpoint:     rec.Endpoint,
		Model:        rec.Model,
		SessionID:    rec.SessionID,
		App:          rec.App,
		RetryCount:   rec.RetryCount,
		AccountID:    rec.AccountID,
		Status:       rec.StatusCode,
		LatencyMS:    rec.LatencyMS,
		TTFTMS:       rec.TTFTMS,
		StopReason:   rec.StopReason,
		Error:        rec.ResponseError,
		InputTokens:  rec.Usage.InputTokens,
		OutputTokens: rec.Usage.OutputTokens,
		CacheRead:    rec.Usage.CacheReadTokens,
		CacheWrite:   rec.Usage.CacheCreationTokens,
		CostUSD:      rec.CostUSD,
		TotalBytes:   rec.TotalBytes,
		ToolCount:    len(rec.Tools),
	}
	for _, b := range rec.ResponseBlocks {
		if b.Kind == "text" {
			l.Response = truncate(b.Text, s.maxLineBytes/2)
			break
		}
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if len(raw) > s.maxLineBytes {
		l.Response = ""
		raw, _ = json.Marshal(l)
	}
	s.batcher.Add(ctx, entry{ts: rec.Timestamp, model: rec.Model, line: raw})
	return nil
}

// push is the Loki HTTP push payload shape.
type push struct {
	Streams []stream `json:"streams"`
}
type stream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

func (s *Sink) send(ctx context.Context, batch []entry) error {
	// Group by model — the only per-record label, kept low-cardinality.
	groups := map[string][][2]string{}
	for _, e := range batch {
		groups[e.model] = append(groups[e.model], [2]string{
			strconv.FormatInt(e.ts.UnixNano(), 10), string(e.line),
		})
	}
	var p push
	for model, values := range groups {
		p.Streams = append(p.Streams, stream{
			Stream: map[string]string{"job": jobLabel, "model": model},
			Values: values,
		})
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return sink.PostChecked(ctx, s.client, s.pushURL, "application/json", bytes.NewReader(body), nil)
}

func (s *Sink) Close(ctx context.Context) error {
	s.batcher.Close(ctx)
	return nil
}

// truncate cuts s to at most n bytes on a rune boundary, so the result is
// still valid UTF-8 inside a JSON log line.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…[truncated]"
}
