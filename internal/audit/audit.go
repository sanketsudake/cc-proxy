// Package audit measures what a /v1/messages request spends its bytes on:
// tool definitions ranked by size, system prompt, and total request size.
package audit

import (
	"encoding/json"
	"sort"
)

// EstTokens is a rough display-only token estimate (~4 bytes/token). Real
// input tokens come from the response usage.
func EstTokens(bytes int) int { return (bytes + 2) / 4 }

// ToolStat is one tool definition's size contribution.
type ToolStat struct {
	Name         string `json:"name"`
	Bytes        int    `json:"bytes"`
	ApproxTokens int    `json:"approx_tokens"`
}

// Result is the size breakdown of one request.
type Result struct {
	Model        string
	Stream       bool
	AccountID    string // account_uuid from metadata.user_id, when sent
	DeviceID     string
	Tools        []ToolStat // sorted by bytes, descending
	ToolsBytes   int
	SystemBytes  int
	TotalBytes   int
	MessageCount int
}

// request mirrors just the fields of an Anthropic /v1/messages body we
// measure; everything else stays raw.
type request struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	System   json.RawMessage   `json:"system"`
	Tools    []json.RawMessage `json:"tools"`
	Messages []json.RawMessage `json:"messages"`
	Metadata *struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

// userID is what Claude Code packs into metadata.user_id: a JSON object
// serialized as a string. Other clients may send anything (or nothing)
// there, so parsing is strictly best-effort.
type userID struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
}

type toolName struct {
	Name string `json:"name"`
}

// Analyze parses the raw request body and ranks its removable regions.
// A non-JSON body yields a zeroed Result with only TotalBytes set.
func Analyze(body []byte) Result {
	res := Result{TotalBytes: len(body)}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return res
	}
	res.Model = req.Model
	res.Stream = req.Stream
	res.MessageCount = len(req.Messages)
	if req.Metadata != nil && req.Metadata.UserID != "" {
		var uid userID
		if err := json.Unmarshal([]byte(req.Metadata.UserID), &uid); err == nil {
			res.AccountID = uid.AccountUUID
			res.DeviceID = uid.DeviceID
		}
	}
	if req.System != nil {
		res.SystemBytes = len(req.System)
	}

	res.Tools = make([]ToolStat, 0, len(req.Tools))
	for _, raw := range req.Tools {
		var tn toolName
		_ = json.Unmarshal(raw, &tn)
		if tn.Name == "" {
			tn.Name = "(unnamed)"
		}
		res.Tools = append(res.Tools, ToolStat{Name: tn.Name, Bytes: len(raw), ApproxTokens: EstTokens(len(raw))})
		res.ToolsBytes += len(raw)
	}
	sort.Slice(res.Tools, func(i, j int) bool { return res.Tools[i].Bytes > res.Tools[j].Bytes })
	return res
}
