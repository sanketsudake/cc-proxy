package sink

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// PostChecked is the shared HTTP shape of the remote sinks: POST a body,
// treat any non-2xx as an error carrying the response's first 2 KB.
func PostChecked(ctx context.Context, client *http.Client, url, contentType string, body io.Reader, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("post %s: status %d: %s", url, resp.StatusCode, msg)
	}
	return nil
}
