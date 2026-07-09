// Package clickhouse ships analytics rows to ClickHouse over its HTTP
// interface (INSERT ... FORMAT JSONEachRow). No client library, no raw
// payloads — just the columns the Grafana dashboards aggregate.
package clickhouse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/config"
	"github.com/sanketsudake/cc-proxy/internal/sink"
)

// ClickHouse HTTP-interface auth headers.
const (
	headerUser = "X-ClickHouse-User"
	headerKey  = "X-ClickHouse-Key"
)

// row is one JSONEachRow line; column names match the DDL in
// deploy/clickhouse/init/01_schema.sql.
type row struct {
	ID                  string   `json:"id"`
	TS                  string   `json:"ts"` // DateTime64(3)
	Endpoint            string   `json:"endpoint"`
	Method              string   `json:"method"`
	Model               string   `json:"model"`
	Status              int      `json:"status"`
	LatencyMS           int64    `json:"latency_ms"`
	TTFTMS              int64    `json:"ttft_ms"`
	Stream              uint8    `json:"stream"`
	Truncated           uint8    `json:"truncated"`
	SessionID           string   `json:"session_id"`
	App                 string   `json:"app"`
	ClientVersion       string   `json:"client_version"`
	RetryCount          int      `json:"retry_count"`
	AccountID           string   `json:"account_id"`
	DeviceID            string   `json:"device_id"`
	SystemBytes         int      `json:"system_bytes"`
	ToolsBytes          int      `json:"tools_bytes"`
	TotalBytes          int      `json:"total_bytes"`
	MessageCount        int      `json:"message_count"`
	StopReason          string   `json:"stop_reason"`
	InputTokens         int      `json:"input_tokens"`
	OutputTokens        int      `json:"output_tokens"`
	CacheReadTokens     int      `json:"cache_read_tokens"`
	CacheCreationTokens int      `json:"cache_creation_tokens"`
	CostUSD             float64  `json:"cost_usd"`
	ToolNames           []string `json:"tools.name"`
	ToolBytes           []int    `json:"tools.bytes"`
}

type Sink struct {
	batcher   *sink.Batcher[row]
	client    *http.Client
	baseURL   string
	insertURL string
	username  string
	password  string
}

// migrations are columns added after the initial DDL, applied idempotently
// at startup so tables created by an older deploy keep accepting inserts.
var migrations = []string{
	"ADD COLUMN IF NOT EXISTS session_id String",
	"ADD COLUMN IF NOT EXISTS app LowCardinality(String)",
	"ADD COLUMN IF NOT EXISTS client_version LowCardinality(String)",
	"ADD COLUMN IF NOT EXISTS retry_count UInt8",
	"ADD COLUMN IF NOT EXISTS account_id LowCardinality(String)",
	"ADD COLUMN IF NOT EXISTS device_id LowCardinality(String)",
}

func New(cfg config.ClickHouseSink, logger *slog.Logger) *Sink {
	query := fmt.Sprintf("INSERT INTO %s.%s FORMAT JSONEachRow", cfg.Database, cfg.Table)
	s := &Sink{
		client:    &http.Client{Timeout: 30 * time.Second},
		baseURL:   cfg.URL,
		insertURL: cfg.URL + "/?query=" + url.QueryEscape(query),
		username:  cfg.Username,
		password:  cfg.Password,
	}
	s.migrate(cfg, logger)
	s.batcher = sink.NewBatcher("clickhouse", cfg.BatchSize, cfg.FlushInterval.Std(), logger, s.send)
	return s
}

// migrate applies the ALTERs best-effort; a down backend is not an error —
// a table created fresh by the compose init script already has the columns.
func (s *Sink) migrate(cfg config.ClickHouseSink, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, m := range migrations {
		stmt := fmt.Sprintf("ALTER TABLE %s.%s %s", cfg.Database, cfg.Table, m)
		if err := s.exec(ctx, stmt); err != nil {
			logger.Debug("clickhouse migration skipped", "stmt", stmt, "err", err)
			return
		}
	}
}

func (s *Sink) exec(ctx context.Context, query string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/", bytes.NewReader([]byte(query)))
	if err != nil {
		return err
	}
	if s.username != "" {
		req.Header.Set(headerUser, s.username)
		req.Header.Set(headerKey, s.password)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("clickhouse exec status %d: %s", resp.StatusCode, msg)
	}
	return nil
}

func (s *Sink) Name() string { return "clickhouse" }

func (s *Sink) Write(ctx context.Context, rec *capture.Record) error {
	r := row{
		ID:                  rec.ID,
		TS:                  rec.Timestamp.UTC().Format("2006-01-02 15:04:05.000"),
		Endpoint:            rec.Endpoint,
		Method:              rec.Method,
		Model:               rec.Model,
		Status:              rec.StatusCode,
		LatencyMS:           rec.LatencyMS,
		TTFTMS:              rec.TTFTMS,
		Stream:              boolByte(rec.Stream),
		Truncated:           boolByte(rec.Truncated),
		SessionID:           rec.SessionID,
		App:                 rec.App,
		ClientVersion:       rec.ClientVersion,
		RetryCount:          rec.RetryCount,
		AccountID:           rec.AccountID,
		DeviceID:            rec.DeviceID,
		SystemBytes:         rec.SystemBytes,
		ToolsBytes:          rec.ToolsBytes,
		TotalBytes:          rec.TotalBytes,
		MessageCount:        rec.MessageCount,
		StopReason:          rec.StopReason,
		InputTokens:         rec.Usage.InputTokens,
		OutputTokens:        rec.Usage.OutputTokens,
		CacheReadTokens:     rec.Usage.CacheReadTokens,
		CacheCreationTokens: rec.Usage.CacheCreationTokens,
		CostUSD:             rec.CostUSD,
	}
	for _, t := range rec.Tools {
		r.ToolNames = append(r.ToolNames, t.Name)
		r.ToolBytes = append(r.ToolBytes, t.Bytes)
	}
	s.batcher.Add(ctx, r)
	return nil
}

func (s *Sink) send(ctx context.Context, batch []row) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, r := range batch {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.insertURL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if s.username != "" {
		req.Header.Set(headerUser, s.username)
		req.Header.Set(headerKey, s.password)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("clickhouse insert status %d: %s", resp.StatusCode, msg)
	}
	return nil
}

func (s *Sink) Close(ctx context.Context) error {
	s.batcher.Close(ctx)
	return nil
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
