package proxy

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// teeBody wraps the upstream response body. Read copies bytes into a memory
// buffer (a pure append — never blocks the client stream); Close stamps the
// end time and fires the capture callback exactly once. ReverseProxy always
// closes the body, including on client disconnect, so the capture never leaks.
type teeBody struct {
	inner   io.ReadCloser
	cap     *Capture
	buf     bytes.Buffer
	maxByte int64
	fire    func(*Capture)
	sawEOF  bool
	once    sync.Once
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if n > 0 {
		if t.cap.FirstByte.IsZero() {
			t.cap.FirstByte = time.Now()
		}
		if int64(t.buf.Len())+int64(n) <= t.maxByte {
			t.buf.Write(p[:n])
		} else {
			t.cap.Truncated = true
		}
	}
	if err == io.EOF {
		t.sawEOF = true
	}
	return n, err
}

func (t *teeBody) Close() error {
	err := t.inner.Close()
	t.once.Do(func() {
		t.cap.End = time.Now()
		if !t.sawEOF {
			// Client went away (or upstream aborted) before the stream ended.
			t.cap.Truncated = true
		}
		t.cap.ResponseBody = t.buf.Bytes()
		t.fire(t.cap)
	})
	return err
}
