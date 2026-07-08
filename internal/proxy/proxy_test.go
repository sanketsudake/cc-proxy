package proxy

import (
	"bufio"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestHandler(t *testing.T, upstreamURL string, onCapture func(*Capture)) *Handler {
	t.Helper()
	h, err := New(Options{
		UpstreamURL:     upstreamURL,
		MaxRequestBytes: 1 << 20,
		MaxCaptureBytes: 1 << 20,
		OnCapture:       onCapture,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestStreamingIsRealTime proves the client receives SSE bytes while the
// upstream is still mid-stream — the proxy must not buffer the response.
func TestStreamingIsRealTime(t *testing.T) {
	clientGotFirst := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
		w.(http.Flusher).Flush()
		// Hold the stream open until the client has read the first event.
		select {
		case <-clientGotFirst:
		case <-time.After(5 * time.Second):
			t.Error("client never saw the first event while stream was open")
		}
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	captured := make(chan *Capture, 1)
	proxySrv := httptest.NewServer(newTestHandler(t, upstream.URL, func(c *Capture) { captured <- c }))
	defer proxySrv.Close()

	resp, err := http.Post(proxySrv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-5","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "event: message_start") {
		t.Fatalf("unexpected first line %q", line)
	}
	close(clientGotFirst) // first event arrived while upstream was still blocked

	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), "message_stop") {
		t.Fatalf("missing tail of stream: %q", rest)
	}

	select {
	case c := <-captured:
		if c.StatusCode != http.StatusOK {
			t.Errorf("status = %d", c.StatusCode)
		}
		if !strings.Contains(string(c.ResponseBody), "message_start") || !strings.Contains(string(c.ResponseBody), "message_stop") {
			t.Errorf("capture missing stream bytes: %q", c.ResponseBody)
		}
		if !strings.Contains(string(c.RequestBody), "claude-sonnet-5") {
			t.Errorf("capture missing request body: %q", c.RequestBody)
		}
		if c.Truncated {
			t.Error("capture unexpectedly truncated")
		}
		if c.FirstByte.IsZero() || c.End.Before(c.Start) {
			t.Error("capture timings not recorded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capture never fired")
	}
}

func TestAuthHeaderForwardedAndCount_tokensSkipped(t *testing.T) {
	var gotAuth, gotAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		_, _ = io.WriteString(w, `{"input_tokens": 42}`)
	}))
	defer upstream.Close()

	captured := make(chan *Capture, 1)
	proxySrv := httptest.NewServer(newTestHandler(t, upstream.URL, func(c *Capture) { captured <- c }))
	defer proxySrv.Close()

	req, _ := http.NewRequest(http.MethodPost, proxySrv.URL+"/v1/messages/count_tokens", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("X-Api-Key", "key-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if gotAuth != "Bearer sk-test" || gotAPIKey != "key-test" {
		t.Errorf("auth not forwarded: auth=%q apikey=%q", gotAuth, gotAPIKey)
	}
	select {
	case <-captured:
		t.Error("count_tokens request should not be captured")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestGzipErrorBody ensures a gzip-encoded upstream JSON body reaches both
// the client and the capture decompressed (we strip Accept-Encoding, so the
// transport transparently decompresses).
func TestGzipErrorBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The client's own Accept-Encoding is stripped in Rewrite; the
		// transport then adds its own "gzip" and transparently decompresses.
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		gz := gzip.NewWriter(w)
		_, _ = io.WriteString(gz, `{"error":{"type":"invalid_request_error"}}`)
		_ = gz.Close()
	}))
	defer upstream.Close()

	captured := make(chan *Capture, 1)
	proxySrv := httptest.NewServer(newTestHandler(t, upstream.URL, func(c *Capture) { captured <- c }))
	defer proxySrv.Close()

	resp, err := http.Post(proxySrv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "invalid_request_error") {
		t.Errorf("client body not decompressed: %q", body)
	}
	c := <-captured
	if !strings.Contains(string(c.ResponseBody), "invalid_request_error") {
		t.Errorf("captured body not decompressed: %q", c.ResponseBody)
	}
}

func TestUpstreamDown(t *testing.T) {
	proxySrv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1", func(*Capture) {}))
	defer proxySrv.Close()

	resp, err := http.Post(proxySrv.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "upstream error") {
		t.Errorf("body = %q", body)
	}
}
