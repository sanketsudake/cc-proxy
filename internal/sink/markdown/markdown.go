// Package markdown writes per-request files, format-compatible with the
// original agent-proxy gist: a readable .md audit document plus the raw
// request body as .request.txt.
package markdown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sanketsudake/claude-agent-proxy/internal/capture"
)

type Sink struct {
	dir string
}

func New(dir string) *Sink { return &Sink{dir: dir} }

func (s *Sink) Name() string { return "markdown" }

func (s *Sink) Write(_ context.Context, rec *capture.Record) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	base := rec.Timestamp.UTC().Format("2006-01-02T15-04-05") + "_" + rec.ID
	if err := os.WriteFile(filepath.Join(s.dir, base+".request.txt"), rec.RawRequest, 0o644); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, base+".md"), []byte(Render(rec)), 0o644); err != nil {
		return fmt.Errorf("write markdown: %w", err)
	}
	return nil
}

func (s *Sink) Close(context.Context) error { return nil }
