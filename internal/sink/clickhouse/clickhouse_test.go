package clickhouse

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/config"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

func testRecord(id string) *capture.Record {
	return &capture.Record{
		ID: id, Timestamp: time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC),
		Endpoint: "/v1/messages", Method: "POST", Model: "claude-sonnet-5",
		StatusCode: 200, LatencyMS: 1000,
		Usage: sse.Usage{InputTokens: 10, OutputTokens: 5},
		Tools: []audit.ToolStat{{Name: "bash", Bytes: 100, ApproxTokens: 25}},
	}
}

func TestBatchInsertShape(t *testing.T) {
	var rows atomic.Int64
	var gotQuery, gotUser atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery.Store(r.URL.Query().Get("query"))
		gotUser.Store(r.Header.Get("X-ClickHouse-User"))
		sc := bufio.NewScanner(r.Body)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("bad JSONEachRow line: %v", err)
			}
			if m["model"] != "claude-sonnet-5" {
				t.Errorf("row model = %v", m["model"])
			}
			rows.Add(1)
		}
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := New(config.ClickHouseSink{
		URL: srv.URL, Database: "claude", Table: "requests",
		Username: "claude", Password: "claude",
		BatchSize: 2, FlushInterval: config.Duration(time.Hour),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Third write triggers a size-based flush of 2; Close flushes the rest.
	for i := range 3 {
		_ = s.Write(context.Background(), testRecord(string(rune('a'+i))))
	}
	_ = s.Close(context.Background())

	if rows.Load() != 3 {
		t.Errorf("rows received = %d, want 3", rows.Load())
	}
	if q := gotQuery.Load().(string); !strings.Contains(q, "INSERT INTO claude.requests FORMAT JSONEachRow") {
		t.Errorf("query = %q", q)
	}
	if u := gotUser.Load().(string); u != "claude" {
		t.Errorf("user header = %q", u)
	}
}

func TestRetryThenDrop(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := New(config.ClickHouseSink{
		URL: srv.URL, Database: "claude", Table: "requests",
		BatchSize: 1, FlushInterval: config.Duration(time.Hour),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = s.Write(context.Background(), testRecord("x")) // flushes immediately (batch size 1)
	_ = s.Close(context.Background())

	if attempts.Load() != 3 {
		t.Errorf("attempts = %d, want 3 (retry with backoff then drop)", attempts.Load())
	}
}
