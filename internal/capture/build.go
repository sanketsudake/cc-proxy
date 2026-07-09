package capture

import (
	"strconv"
	"strings"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/cost"
	"github.com/sanketsudake/cc-proxy/internal/proxy"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

// Builder converts raw proxy captures into Records.
type Builder struct {
	Estimator *cost.Estimator
}

// Build parses, audits, and prices one exchange.
func (b *Builder) Build(c *proxy.Capture) *Record {
	a := audit.Analyze(c.RequestBody)
	resp := sse.Decode(c.ResponseBody, c.ResponseHeader.Get("Content-Type"))

	model := resp.Model
	if model == "" {
		model = a.Model
	}

	rec := &Record{
		ID:         NewID(c.Start),
		Timestamp:  c.Start,
		Endpoint:   c.Path,
		Method:     c.Method,
		Model:      model,
		StatusCode: c.StatusCode,
		LatencyMS:  c.End.Sub(c.Start).Milliseconds(),
		Stream:     a.Stream || strings.Contains(c.ResponseHeader.Get("Content-Type"), "event-stream"),
		Truncated:  c.Truncated,
		Headers:    RedactHeaders(c.RequestHeader),

		SessionID:     c.RequestHeader.Get("X-Claude-Code-Session-Id"),
		App:           c.RequestHeader.Get("X-App"),
		ClientVersion: c.RequestHeader.Get("User-Agent"),
		AccountID:     a.AccountID,
		DeviceID:      a.DeviceID,

		SystemBytes:  a.SystemBytes,
		TotalBytes:   a.TotalBytes,
		Tools:        a.Tools,
		ToolsBytes:   a.ToolsBytes,
		MessageCount: a.MessageCount,

		StopReason:     resp.StopReason,
		ResponseError:  resp.Error,
		Usage:          resp.Usage,
		RawRequest:     c.RequestBody,
		ResponseBlocks: resp.Blocks,
	}
	if !c.FirstByte.IsZero() {
		rec.TTFTMS = c.FirstByte.Sub(c.Start).Milliseconds()
	}
	if v := c.RequestHeader.Get("X-Stainless-Retry-Count"); v != "" {
		rec.RetryCount, _ = strconv.Atoi(v)
	}
	if b.Estimator != nil {
		rec.CostUSD, rec.CostKnown = b.Estimator.Estimate(model, resp.Usage)
	}
	return rec
}
