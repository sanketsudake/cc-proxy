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
	Usage *Usage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Decode picks the right decoder from the response content type.
func Decode(body []byte, contentType string) Response {
	if strings.Contains(contentType, "text/event-stream") {
		return DecodeStream(body)
	}
	return DecodeJSON(body)
}

// DecodeStream reassembles an SSE stream. Unknown event and block types are
// tolerated (Anthropic adds new ones over time) — their deltas accumulate
// into whatever text fields they carry.
func DecodeStream(raw []byte) Response {
	var resp Response
	blocks := map[int]*Block{}
	maxIdx := -1

	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
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
			b := &Block{Kind: "text"}
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
				b.Text += ev.Delta.Text + ev.Delta.PartialJSON + ev.Delta.Thinking
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
				resp.Error = ev.Error.Type + ": " + ev.Error.Message
			}
		}
	}
	for i := 0; i <= maxIdx; i++ {
		if b, ok := blocks[i]; ok {
			resp.Blocks = append(resp.Blocks, *b)
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
	Usage *Usage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
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
		resp.Error = msg.Error.Type + ": " + msg.Error.Message
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
