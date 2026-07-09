package capture

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sanketsudake/cc-proxy/internal/audit"
	"github.com/sanketsudake/cc-proxy/internal/cost"
	"github.com/sanketsudake/cc-proxy/internal/proxy"
	"github.com/sanketsudake/cc-proxy/internal/sse"
)

// Headers Claude Code sends on every request, used for attribution.
const (
	HeaderSessionID  = "X-Claude-Code-Session-Id"
	HeaderApp        = "X-App"
	HeaderUserAgent  = "User-Agent"
	HeaderRetryCount = "X-Stainless-Retry-Count"
)

// userID is what Claude Code packs into metadata.user_id: a JSON object
// serialized as a string. Other clients may send anything (or nothing)
// there, so parsing is strictly best-effort.
type userID struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
}

func parseUserID(raw string) userID {
	var uid userID
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &uid)
	}
	return uid
}

// Builder converts raw proxy captures into Records.
type Builder struct {
	Estimator *cost.Estimator
}

// Build parses, audits, and prices one exchange.
func (b *Builder) Build(c *proxy.Capture) *Record {
	a := audit.Analyze(c.RequestBody)
	uid := parseUserID(a.MetadataUserID)
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

		SessionID:     c.RequestHeader.Get(HeaderSessionID),
		App:           c.RequestHeader.Get(HeaderApp),
		ClientVersion: c.RequestHeader.Get(HeaderUserAgent),
		AccountID:     uid.AccountUUID,
		DeviceID:      uid.DeviceID,

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
	if v := c.RequestHeader.Get(HeaderRetryCount); v != "" {
		rec.RetryCount, _ = strconv.Atoi(v)
	}
	if b.Estimator != nil {
		rec.CostUSD, rec.CostKnown = b.Estimator.Estimate(model, resp.Usage)
	}
	return rec
}
