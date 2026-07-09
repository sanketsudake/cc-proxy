// Package sqlite persists capture records to an embedded SQLite database
// (pure-Go driver, no CGO) for local ad-hoc querying.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sanketsudake/cc-proxy/internal/capture"
)

//go:embed schema.sql
var schema string

type Sink struct {
	db *sql.DB
}

// New opens (or creates) the database and applies the schema.
func New(path string) (*Sink, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// The dispatcher gives each sink a single writer goroutine; one
	// connection avoids SQLITE_BUSY entirely.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	migrate(db)
	return &Sink{db: db}, nil
}

// migrations are columns added after the initial schema. CREATE TABLE IF NOT
// EXISTS skips existing databases, so each is applied idempotently here —
// "duplicate column name" just means the database is already current.
var migrations = []string{
	`ALTER TABLE requests ADD COLUMN session_id TEXT`,
	`ALTER TABLE requests ADD COLUMN app TEXT`,
	`ALTER TABLE requests ADD COLUMN client_version TEXT`,
	`ALTER TABLE requests ADD COLUMN retry_count INTEGER`,
	`ALTER TABLE requests ADD COLUMN account_id TEXT`,
	`ALTER TABLE requests ADD COLUMN device_id TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id)`,
}

func migrate(db *sql.DB) {
	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			// Anything else is unexpected but not fatal: the insert will
			// surface a real problem loudly.
			continue
		}
	}
}

func (s *Sink) Name() string { return "sqlite" }

func (s *Sink) Write(ctx context.Context, rec *capture.Record) error {
	headers, _ := json.Marshal(rec.Headers)
	response, _ := json.Marshal(rec.ResponseBlocks)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO requests (
		id, ts, endpoint, method, model, status, latency_ms, ttft_ms, stream, truncated,
		session_id, app, client_version, retry_count, account_id, device_id,
		system_bytes, tools_bytes, total_bytes, message_count,
		stop_reason, response_error,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, cost_usd,
		headers_json, raw_request, response_json
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.Timestamp.UTC().Format(time.RFC3339Nano), rec.Endpoint, rec.Method, rec.Model,
		rec.StatusCode, rec.LatencyMS, rec.TTFTMS, rec.Stream, rec.Truncated,
		rec.SessionID, rec.App, rec.ClientVersion, rec.RetryCount, rec.AccountID, rec.DeviceID,
		rec.SystemBytes, rec.ToolsBytes, rec.TotalBytes, rec.MessageCount,
		rec.StopReason, rec.ResponseError,
		rec.Usage.InputTokens, rec.Usage.OutputTokens, rec.Usage.CacheReadTokens, rec.Usage.CacheCreationTokens,
		rec.CostUSD, string(headers), rec.RawRequest, string(response),
	)
	if err != nil {
		return fmt.Errorf("insert request: %w", err)
	}
	for _, t := range rec.Tools {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO request_tools (request_id, name, bytes, approx_tokens) VALUES (?,?,?,?)`,
			rec.ID, t.Name, t.Bytes, t.ApproxTokens); err != nil {
			return fmt.Errorf("insert tool: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Sink) Close(context.Context) error { return s.db.Close() }
