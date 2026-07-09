// Package sse decodes Anthropic Messages API responses — both streamed SSE
// and plain JSON bodies — into a normalized shape for the capture pipeline.
package sse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// Usage mirrors the Anthropic usage object, accumulated across stream events.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

// TotalInputTokens is what the request actually consumed on the input side.
func (u Usage) TotalInputTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

// Block is one assembled content block from the response.
type Block struct {
	Kind string // "text" | "thinking" | "tool_use" | anything future
	Name string // tool name for tool_use
	ID   string // tool_use id
	Text string // text, thinking text, or accumulated tool-input JSON
}

// Response is the decoded reply.
type Response struct {
	Model      string
	StopReason string
	Blocks     []Block
	Usage      Usage
	Error      string // non-empty if the body carried an API error
}

// apiError is the error object Anthropic embeds in SSE error events and
// JSON error bodies.
type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e apiError) String() string { return e.Type + ": " + e.Message }

type event struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Model string `json:"model"`
		Usage *Usage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *Usage    `json:"usage"`
	Error *apiError `json:"error"`
}

// Decode picks the right decoder from the response content type.
func Decode(body []byte, contentType string) Response {
	if strings.Contains(contentType, "text/event-stream") {
		return DecodeStream(body)
	}
	return DecodeJSON(body)
}

// partialBlock accumulates a block's deltas in a strings.Builder: long
// assistant turns arrive as thousands of small deltas, and naive string
// concatenation would copy the accumulated text on every one.
type partialBlock struct {
	Block
	text strings.Builder
}

// scanBufMax bounds one SSE line. It must be at least the proxy's response
// capture cap (config.DefaultMaxCaptureBytes) or a single huge data: line
// could not be decoded even though the proxy captured it.
const scanBufMax = 64 << 20

// DecodeStream reassembles an SSE stream. Unknown event and block types are
// tolerated (Anthropic adds new ones over time) — their deltas accumulate
// into whatever text fields they carry.
func DecodeStream(raw []byte) Response {
	var resp Response
	blocks := map[int]*partialBlock{}
	maxIdx := -1

	dataPrefix := []byte("data:")
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), scanBufMax)
	for sc.Scan() {
		data, ok := bytes.CutPrefix(sc.Bytes(), dataPrefix)
		if !ok {
			continue
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		var ev event
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				resp.Model = ev.Message.Model
				if ev.Message.Usage != nil {
					mergeUsage(&resp.Usage, *ev.Message.Usage)
				}
			}
		case "content_block_start":
			b := &partialBlock{Block: Block{Kind: "text"}}
			if ev.ContentBlock != nil {
				b.Kind = ev.ContentBlock.Type
				b.Name = ev.ContentBlock.Name
				b.ID = ev.ContentBlock.ID
			}
			blocks[ev.Index] = b
			if ev.Index > maxIdx {
				maxIdx = ev.Index
			}
		case "content_block_delta":
			if b, ok := blocks[ev.Index]; ok && ev.Delta != nil {
				b.text.WriteString(ev.Delta.Text)
				b.text.WriteString(ev.Delta.PartialJSON)
				b.text.WriteString(ev.Delta.Thinking)
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				resp.StopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				mergeUsage(&resp.Usage, *ev.Usage)
			}
		case "error":
			if ev.Error != nil {
				resp.Error = ev.Error.String()
			}
		}
	}
	if err := sc.Err(); err != nil && resp.Error == "" {
		// Scanner overflow or similar: everything after the failing line is
		// lost, so say so instead of silently returning a partial decode.
		resp.Error = "sse decode aborted: " + err.Error()
	}
	for i := 0; i <= maxIdx; i++ {
		if b, ok := blocks[i]; ok {
			b.Text = b.text.String()
			resp.Blocks = append(resp.Blocks, b.Block)
		}
	}
	return resp
}

// jsonMessage mirrors a non-streamed /v1/messages response (or error body).
type jsonMessage struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		Name     string          `json:"name"`
		ID       string          `json:"id"`
		Input    json.RawMessage `json:"input"`
	} `json:"content"`
	Usage *Usage    `json:"usage"`
	Error *apiError `json:"error"`
}

// DecodeJSON handles stream:false replies and JSON error bodies.
func DecodeJSON(raw []byte) Response {
	var resp Response
	var msg jsonMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return resp
	}
	resp.Model = msg.Model
	resp.StopReason = msg.StopReason
	if msg.Usage != nil {
		resp.Usage = *msg.Usage
	}
	if msg.Error != nil {
		resp.Error = msg.Error.String()
	}
	for _, c := range msg.Content {
		b := Block{Kind: c.Type, Name: c.Name, ID: c.ID}
		switch c.Type {
		case "text":
			b.Text = c.Text
		case "thinking":
			b.Text = c.Thinking
		case "tool_use":
			b.Text = string(c.Input)
		default:
			b.Text = c.Text
		}
		resp.Blocks = append(resp.Blocks, b)
	}
	return resp
}

// mergeUsage overwrites only positive fields: message_start carries the
// input-side counts while message_delta carries output_tokens, and each
// leaves the other's fields zeroed — a plain assignment would clobber them.
func mergeUsage(dst *Usage, src Usage) {
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheReadTokens > 0 {
		dst.CacheReadTokens = src.CacheReadTokens
	}
	if src.CacheCreationTokens > 0 {
		dst.CacheCreationTokens = src.CacheCreationTokens
	}
}
