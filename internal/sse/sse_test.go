package sse

import (
	"strings"
	"testing"
)

const stream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","usage":{"input_tokens":100,"cache_read_input_tokens":5000,"cache_creation_input_tokens":200}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello "}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world"}}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Pune\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}
`

func TestDecodeStream(t *testing.T) {
	r := DecodeStream([]byte(stream))
	if r.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", r.Model)
	}
	if r.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q", r.StopReason)
	}
	u := r.Usage
	if u.InputTokens != 100 || u.OutputTokens != 42 || u.CacheReadTokens != 5000 || u.CacheCreationTokens != 200 {
		t.Errorf("usage = %+v", u)
	}
	if u.TotalInputTokens() != 5300 {
		t.Errorf("total input = %d", u.TotalInputTokens())
	}
	if len(r.Blocks) != 3 {
		t.Fatalf("blocks = %+v", r.Blocks)
	}
	if r.Blocks[0].Kind != "thinking" || r.Blocks[0].Text != "pondering" {
		t.Errorf("thinking block = %+v", r.Blocks[0])
	}
	if r.Blocks[1].Text != "Hello world" {
		t.Errorf("text block = %+v", r.Blocks[1])
	}
	if r.Blocks[2].Kind != "tool_use" || r.Blocks[2].Name != "get_weather" || r.Blocks[2].Text != `{"city":"Pune"}` {
		t.Errorf("tool block = %+v", r.Blocks[2])
	}
}

func TestDecodeStreamError(t *testing.T) {
	raw := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"
	r := DecodeStream([]byte(raw))
	if !strings.Contains(r.Error, "overloaded_error") {
		t.Errorf("error = %q", r.Error)
	}
}

func TestDecodeStreamMalformedLines(t *testing.T) {
	raw := "data: not-json\n\ndata: [DONE]\n\ngarbage line\n" + stream
	r := DecodeStream([]byte(raw))
	if r.Model != "claude-sonnet-5" || len(r.Blocks) != 3 {
		t.Errorf("malformed lines broke decode: %+v", r)
	}
}

func TestDecodeJSON(t *testing.T) {
	raw := `{"model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5}}`
	r := Decode([]byte(raw), "application/json")
	if r.Model != "claude-opus-4-8" || r.StopReason != "end_turn" {
		t.Errorf("decoded = %+v", r)
	}
	if len(r.Blocks) != 1 || r.Blocks[0].Text != "hi" {
		t.Errorf("blocks = %+v", r.Blocks)
	}
}

func TestDecodeJSONError(t *testing.T) {
	raw := `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`
	r := Decode([]byte(raw), "application/json")
	if !strings.Contains(r.Error, "invalid_request_error") {
		t.Errorf("error = %q", r.Error)
	}
}

func TestContentTypeRouting(t *testing.T) {
	r := Decode([]byte(stream), "text/event-stream; charset=utf-8")
	if r.Model != "claude-sonnet-5" {
		t.Errorf("SSE routing failed: %+v", r)
	}
}
