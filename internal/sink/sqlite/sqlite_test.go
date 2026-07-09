package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

func TestWriteAndQuery(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	rec := &capture.Record{
		ID:         "req-1",
		Timestamp:  time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC),
		Endpoint:   "/v1/messages",
		Method:     "POST",
		Model:      "claude-sonnet-5",
		StatusCode: 200,
		LatencyMS:  1200,
		Usage:      sse.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 4000},
		CostUSD:    0.01,
		Tools: []audit.ToolStat{
			{Name: "bash", Bytes: 500, ApproxTokens: 125},
			{Name: "read", Bytes: 300, ApproxTokens: 75},
		},
		Headers:    map[string]string{"Authorization": "[REDACTED]"},
		RawRequest: []byte(`{"model":"claude-sonnet-5"}`),
		SessionID:  "sess-abc",
		App:        "cli",
		RetryCount: 1,
		AccountID:  "acct-9",
		DeviceID:   "dev-1",
	}
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	// Re-delivery of the same id must not error, and the requests row must
	// stay unique (asserted below).
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatalf("re-delivery: %v", err)
	}

	var model string
	var input int
	if err := s.db.QueryRow(`SELECT model, input_tokens FROM requests WHERE id = 'req-1'`).Scan(&model, &input); err != nil {
		t.Fatal(err)
	}
	if model != "claude-sonnet-5" || input != 100 {
		t.Errorf("row = %s/%d", model, input)
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("requests rows = %d, want 1", n)
	}

	var tools int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT name) FROM request_tools WHERE request_id = 'req-1'`).Scan(&tools); err != nil {
		t.Fatal(err)
	}
	if tools != 2 {
		t.Errorf("tool rows = %d, want 2", tools)
	}

	var session, app, account string
	var retries int
	if err := s.db.QueryRow(`SELECT session_id, app, retry_count, account_id FROM requests WHERE id = 'req-1'`).Scan(&session, &app, &retries, &account); err != nil {
		t.Fatal(err)
	}
	if session != "sess-abc" || app != "cli" || retries != 1 || account != "acct-9" {
		t.Errorf("attribution = %s/%s/%d/%s", session, app, retries, account)
	}
}

// TestMigrateOldDatabase opens a database created before the attribution
// columns existed and verifies New() adds them.
func TestMigrateOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-migration schema by dropping the new columns.
	if _, err := old.db.Exec(`DROP INDEX idx_requests_session`); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"session_id", "app", "client_version", "retry_count", "account_id", "device_id"} {
		if _, err := old.db.Exec(`ALTER TABLE requests DROP COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	rec := &capture.Record{ID: "m1", Timestamp: time.Now(), Model: "m", SessionID: "sess-xyz"}
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatalf("write after migration: %v", err)
	}
	var session string
	if err := s.db.QueryRow(`SELECT session_id FROM requests WHERE id = 'm1'`).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if session != "sess-xyz" {
		t.Errorf("session = %q", session)
	}
}
