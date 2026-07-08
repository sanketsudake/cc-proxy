package loki

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/config"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

func TestPushShape(t *testing.T) {
	type payload struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}
	got := make(chan payload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/push" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var p payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode: %v", err)
		}
		got <- p
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := New(config.LokiSink{URL: srv.URL, BatchSize: 1, FlushInterval: config.Duration(time.Hour)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := &capture.Record{
		ID: "r1", Timestamp: time.Now(), Endpoint: "/v1/messages",
		Model: "claude-opus-4-8", StatusCode: 200,
		Usage:          sse.Usage{InputTokens: 7, OutputTokens: 3},
		ResponseBlocks: []sse.Block{{Kind: "text", Text: strings.Repeat("x", 100)}},
	}
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	_ = s.Close(context.Background())

	p := <-got
	if len(p.Streams) != 1 {
		t.Fatalf("streams = %d", len(p.Streams))
	}
	st := p.Streams[0]
	if st.Stream["job"] != "cc-proxy" || st.Stream["model"] != "claude-opus-4-8" {
		t.Errorf("labels = %v", st.Stream)
	}
	if len(st.Values) != 1 {
		t.Fatalf("values = %d", len(st.Values))
	}
	var l map[string]any
	if err := json.Unmarshal([]byte(st.Values[0][1]), &l); err != nil {
		t.Fatalf("line not JSON: %v", err)
	}
	if l["id"] != "r1" || l["input_tokens"].(float64) != 7 {
		t.Errorf("line = %v", l)
	}
}

func TestLineTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	s := New(config.LokiSink{URL: srv.URL, MaxLineBytes: 512, BatchSize: 10, FlushInterval: config.Duration(time.Hour)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := &capture.Record{
		ID: "big", Timestamp: time.Now(), Model: "m",
		ResponseBlocks: []sse.Block{{Kind: "text", Text: strings.Repeat("y", 10_000)}},
	}
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	s.batcher.Flush(context.Background())
	_ = s.Close(context.Background())
}
