package audit

import "testing"

const sampleRequest = `{
  "model": "claude-sonnet-5",
  "stream": true,
  "system": [{"type": "text", "text": "You are a helpful assistant."}],
  "tools": [
    {"name": "small", "description": "s", "input_schema": {"type": "object"}},
    {"name": "big", "description": "a much longer description that makes this tool the largest by bytes in the whole request", "input_schema": {"type": "object", "properties": {"a": {"type": "string"}, "b": {"type": "number"}}}}
  ],
  "messages": [
    {"role": "user", "content": "hi"},
    {"role": "assistant", "content": "hello"}
  ]
}`

func TestAnalyze(t *testing.T) {
	a := Analyze([]byte(sampleRequest))
	if a.Model != "claude-sonnet-5" || !a.Stream {
		t.Errorf("model/stream = %q/%v", a.Model, a.Stream)
	}
	if a.MessageCount != 2 {
		t.Errorf("messages = %d", a.MessageCount)
	}
	if len(a.Tools) != 2 || a.Tools[0].Name != "big" {
		t.Fatalf("tools not ranked by size: %+v", a.Tools)
	}
	if a.Tools[0].Bytes <= a.Tools[1].Bytes {
		t.Error("ranking order wrong")
	}
	if a.SystemBytes == 0 || a.ToolsBytes == 0 || a.TotalBytes != len(sampleRequest) {
		t.Errorf("bytes: system=%d tools=%d total=%d", a.SystemBytes, a.ToolsBytes, a.TotalBytes)
	}
}

func TestAnalyzeNonJSON(t *testing.T) {
	a := Analyze([]byte("not json"))
	if a.TotalBytes != 8 || len(a.Tools) != 0 {
		t.Errorf("unexpected result: %+v", a)
	}
}

func TestAnalyzeMetadataUserID(t *testing.T) {
	body := `{"model":"m","metadata":{"user_id":"{\"device_id\":\"dev-1\",\"account_uuid\":\"acct-9\",\"session_id\":\"s-1\"}"}}`
	a := Analyze([]byte(body))
	if a.AccountID != "acct-9" || a.DeviceID != "dev-1" {
		t.Errorf("account/device = %q/%q", a.AccountID, a.DeviceID)
	}
}

func TestAnalyzeMetadataAbsentOrMalformed(t *testing.T) {
	for _, body := range []string{
		`{"model":"m"}`,                                   // no metadata
		`{"model":"m","metadata":{}}`,                     // no user_id
		`{"model":"m","metadata":{"user_id":"not json"}}`, // opaque string
		`{"model":"m","metadata":{"user_id":"[1,2,3]"}}`,  // wrong JSON shape
		`{"model":"m","metadata":{"user_id":""}}`,         // empty
	} {
		a := Analyze([]byte(body))
		if a.AccountID != "" || a.DeviceID != "" {
			t.Errorf("body %s: expected empty attribution, got %q/%q", body, a.AccountID, a.DeviceID)
		}
	}
}

func TestAnalyzeUnnamedTool(t *testing.T) {
	a := Analyze([]byte(`{"tools": [{"description": "no name"}]}`))
	if len(a.Tools) != 1 || a.Tools[0].Name != "(unnamed)" {
		t.Errorf("unnamed tool: %+v", a.Tools)
	}
}
