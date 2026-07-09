// Package capture turns raw proxied bytes into the normalized Record that
// every sink consumes.
package capture

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

// Record is the single normalized capture of one proxied exchange — the
// contract between the pipeline and all sinks.
type Record struct {
	ID        string
	Timestamp time.Time
	Endpoint  string
	Method    string
	Model     string // request model; response model wins when present

	StatusCode int
	LatencyMS  int64
	TTFTMS     int64 // time to first response byte; 0 if none arrived
	Stream     bool
	Truncated  bool

	// Client attribution, from headers Claude Code sends on every request.
	SessionID     string // X-Claude-Code-Session-Id
	App           string // X-App, e.g. "cli"
	ClientVersion string // User-Agent, e.g. "claude-cli/2.1.204 (external, cli)"
	RetryCount    int    // X-Stainless-Retry-Count: >0 means the SDK retried

	Headers map[string]string // redacted

	// Request-side audit
	SystemBytes  int
	TotalBytes   int
	Tools        []audit.ToolStat
	ToolsBytes   int
	MessageCount int

	// Response side
	StopReason    string
	ResponseError string
	Usage         sse.Usage
	CostUSD       float64
	CostKnown     bool

	// Payloads (markdown/SQLite sinks; never shipped to ClickHouse)
	RawRequest     []byte
	ResponseBlocks []sse.Block
}

var redacted = map[string]bool{
	"authorization": true,
	"x-api-key":     true,
	"api-key":       true,
	"cookie":        true,
}

// RedactHeaders flattens headers, masking credential-bearing ones.
func RedactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		if redacted[strings.ToLower(k)] {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = strings.Join(vs, ", ")
	}
	return out
}

// NewID returns a sortable unique id: unix-millis hex + random suffix.
func NewID(t time.Time) string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%012x-%s", t.UnixMilli(), hex.EncodeToString(b[:]))
}
