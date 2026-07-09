package markdown

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/capture"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

var update = flag.Bool("update", false, "regenerate golden files")

func fixtureRecord() *capture.Record {
	raw := `{
  "model": "claude-sonnet-5",
  "stream": true,
  "system": [{"type": "text", "text": "You are a helpful assistant.", "cache_control": {"type": "ephemeral"}}],
  "tools": [
    {"name": "get_weather", "description": "Get the weather", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}},
    {"name": "tiny", "input_schema": {"type": "object"}}
  ],
  "messages": [
    {"role": "user", "content": "What is the weather in Pune?"},
    {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Pune"}}]},
    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "is_error": false, "content": "31C, sunny"}]}
  ]
}`
	a := audit.Analyze([]byte(raw))
	return &capture.Record{
		ID:           "000000000000-fixture",
		Timestamp:    time.Date(2026, 7, 8, 10, 30, 0, 0, time.UTC),
		Endpoint:     "/v1/messages",
		Method:       "POST",
		Model:        "claude-sonnet-5",
		StatusCode:   200,
		LatencyMS:    4200,
		TTFTMS:       350,
		Stream:       true,
		Headers:      map[string]string{"Authorization": "[REDACTED]", "Content-Type": "application/json"},
		SystemBytes:  a.SystemBytes,
		TotalBytes:   a.TotalBytes,
		Tools:        a.Tools,
		ToolsBytes:   a.ToolsBytes,
		MessageCount: a.MessageCount,
		SessionID:    "30ec0161-8163-4519-8956-3daf3c9e5fe5",
		App:          "cli",
		AccountID:    "9cd28a77-affa-45b5-b283-371d893ce498",
		StopReason:   "end_turn",
		Usage:        sse.Usage{InputTokens: 120, OutputTokens: 48, CacheReadTokens: 9000, CacheCreationTokens: 100},
		CostUSD:      0.0123,
		CostKnown:    true,
		RawRequest:   []byte(raw),
		ResponseBlocks: []sse.Block{
			{Kind: "thinking", Text: "considering the tool result"},
			{Kind: "text", Text: "It is 31C and sunny in Pune."},
		},
	}
}

func TestRenderGolden(t *testing.T) {
	got := Render(fixtureRecord())
	golden := filepath.Join("testdata", "fixture.md.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to generate): %v", err)
	}
	if got != string(want) {
		t.Errorf("render mismatch with golden file; run `go test ./internal/sink/markdown -update` after intentional changes\n--- got ---\n%s", got)
	}
}

func TestRenderContainsKeySections(t *testing.T) {
	out := Render(fixtureRecord())
	for _, want := range []string{
		"<meta>", "<audit>", "<headers>", "<system-prompt>", "<tools>", "<messages>", "<response>",
		"**9,220 input tokens**",
		"- **session**: 30ec0161-8163-4519-8956-3daf3c9e5fe5 (cli)",
		"- **account**: 9cd28a77-affa-45b5-b283-371d893ce498",
		"| get_weather |",
		"Authorization: [REDACTED]",
		"<!-- cache_control breakpoint -->",
		"<tool-use name=\"get_weather\"",
		"<tool-result tool-use-id=\"toolu_1\"",
		"<assistant-text>",
		"It is 31C and sunny in Pune.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered markdown missing %q", want)
		}
	}
}

func TestSinkWritesFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.Write(context.Background(), fixtureRecord()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var md, req bool
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			md = true
		}
		if strings.HasSuffix(e.Name(), ".request.txt") {
			req = true
		}
	}
	if !md || !req {
		t.Errorf("expected .md and .request.txt, got %v", entries)
	}
}
