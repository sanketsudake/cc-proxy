// Package proxy implements the transparent reverse proxy in front of the
// Anthropic API, teeing request/response bytes to a capture callback without
// ever delaying the client stream.
package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Capture holds the raw bytes and timings of one proxied exchange. It is
// handed to the OnCapture callback after the response fully streams (or the
// client disconnects); all parsing happens downstream.
type Capture struct {
	Start          time.Time
	End            time.Time
	FirstByte      time.Time // zero if no response byte arrived
	Method         string
	Path           string
	RequestHeader  http.Header
	RequestBody    []byte
	StatusCode     int
	ResponseHeader http.Header
	ResponseBody   []byte
	Truncated      bool // response exceeded capture cap or client disconnected mid-stream
}

// Options configures the proxy handler.
type Options struct {
	UpstreamURL     string
	MaxRequestBytes int64
	MaxCaptureBytes int64
	// OnCapture is invoked once per captured exchange from the response
	// goroutine. It must not block; hand off to a queue immediately.
	OnCapture func(*Capture)
	Logger    *slog.Logger
}

// HealthzPath is where Healthz should be registered.
const HealthzPath = "/healthz"

// countTokensPathSuffix marks token-counting housekeeping calls, which are
// proxied but never captured.
const countTokensPathSuffix = "/count_tokens"

type captureKey struct{}

// Handler proxies requests to the upstream and tees captured exchanges.
type Handler struct {
	rp   *httputil.ReverseProxy
	opts Options
}

// New builds the proxy handler.
func New(opts Options) (*Handler, error) {
	upstream, err := url.Parse(opts.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	h := &Handler{opts: opts}
	h.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Host = upstream.Host
			// Force identity/transparent-gzip so captured bytes are readable.
			pr.Out.Header.Del("Accept-Encoding")
		},
		// Flush every write immediately: SSE tokens must reach the client
		// with no added latency.
		FlushInterval:  -1,
		Transport:      newTransport(),
		ModifyResponse: h.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			opts.Logger.Error("upstream error", "path", r.URL.Path, "err", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "cc-proxy upstream error: " + err.Error(),
			})
		},
		ErrorLog: slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
	}
	return h, nil
}

func newTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2: true,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		// Long enough for Anthropic-side queueing; no overall response
		// timeout because streams legitimately run for minutes.
		ResponseHeaderTimeout: 120 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	}
}

// skipCapture reports whether an exchange should pass through unrecorded.
// count_tokens calls fire constantly as housekeeping and never carry a reply
// worth reading.
func skipCapture(path string) bool {
	return strings.HasSuffix(path, countTokensPathSuffix)
}

// Healthz answers wrapper-script liveness checks; register it on the mux in
// front of the proxy handler so it is never forwarded upstream.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok","service":"cc-proxy"}`))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if skipCapture(r.URL.Path) || h.opts.OnCapture == nil {
		h.rp.ServeHTTP(w, r)
		return
	}

	// Buffer the request body so the audit can read it. Claude Code bodies
	// are bounded JSON, never streamed. Oversized bodies proxy uncaptured.
	body, err := readBody(r, h.opts.MaxRequestBytes)
	if err != nil {
		h.opts.Logger.Error("read request body", "path", r.URL.Path, "err", err)
		http.Error(w, "cc-proxy: failed to read request body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > h.opts.MaxRequestBytes {
		h.opts.Logger.Warn("request body exceeds capture cap, proxying uncaptured",
			"path", r.URL.Path, "cap_bytes", h.opts.MaxRequestBytes)
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		r.ContentLength = -1
		h.rp.ServeHTTP(w, r)
		return
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	capt := &Capture{
		Start:         time.Now(),
		Method:        r.Method,
		Path:          r.URL.Path,
		RequestHeader: r.Header.Clone(),
		RequestBody:   body,
	}
	h.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), captureKey{}, capt)))
}

// readBody buffers up to max+1 bytes, pre-sizing from Content-Length so
// multi-MB Claude Code bodies land in one allocation instead of growing
// through a dozen doublings.
func readBody(r *http.Request, max int64) ([]byte, error) {
	var buf bytes.Buffer
	if r.ContentLength > 0 && r.ContentLength <= max {
		buf.Grow(int(r.ContentLength))
	}
	if _, err := buf.ReadFrom(io.LimitReader(r.Body, max+1)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// modifyResponse wraps the upstream body so bytes are recorded as they stream
// through, and the capture fires when the body closes.
func (h *Handler) modifyResponse(resp *http.Response) error {
	capt, ok := resp.Request.Context().Value(captureKey{}).(*Capture)
	if !ok {
		return nil
	}
	capt.StatusCode = resp.StatusCode
	capt.ResponseHeader = resp.Header.Clone()
	tee := &teeBody{
		inner:   resp.Body,
		capt:    capt,
		maxByte: h.opts.MaxCaptureBytes,
		fire:    h.opts.OnCapture,
	}
	// Pre-size the capture buffer so the tee's Read path (which feeds the
	// client stream) appends without repeated grow-and-copy cycles. SSE
	// responses carry no Content-Length; use a floor that covers most turns.
	grow := int64(256 << 10)
	if resp.ContentLength > 0 {
		grow = resp.ContentLength
	}
	tee.buf.Grow(int(min(grow, h.opts.MaxCaptureBytes)))
	resp.Body = tee
	return nil
}
