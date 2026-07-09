package config

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPathsAnchoredToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	wantData := filepath.Join(home, DefaultDataDirName)
	if cfg.DataDir != wantData {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, wantData)
	}
	if cfg.Sinks.Markdown.Dir != filepath.Join(wantData, DefaultLogsDirName) {
		t.Errorf("Markdown.Dir = %q", cfg.Sinks.Markdown.Dir)
	}
	if cfg.Sinks.SQLite.Path != filepath.Join(wantData, DefaultDBFileName) {
		t.Errorf("SQLite.Path = %q", cfg.Sinks.SQLite.Path)
	}
}

func TestDataDirOverride(t *testing.T) {
	t.Setenv(EnvDataDir, "/tmp/custom-data")
	cfg, err := Load(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sinks.Markdown.Dir != "/tmp/custom-data/logs" || cfg.Sinks.SQLite.Path != "/tmp/custom-data/cc-proxy.db" {
		t.Errorf("paths = %q / %q", cfg.Sinks.Markdown.Dir, cfg.Sinks.SQLite.Path)
	}
}

func TestExplicitSinkPathsWinOverDataDir(t *testing.T) {
	t.Setenv(EnvDataDir, "/tmp/custom-data")
	t.Setenv(EnvSQLitePath, "/elsewhere/db.sqlite")
	cfg, err := Load([]string{"--log-dir", "/elsewhere/logs"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sinks.SQLite.Path != "/elsewhere/db.sqlite" {
		t.Errorf("SQLite.Path = %q", cfg.Sinks.SQLite.Path)
	}
	if cfg.Sinks.Markdown.Dir != "/elsewhere/logs" {
		t.Errorf("Markdown.Dir = %q", cfg.Sinks.Markdown.Dir)
	}
}

func TestInvalidQueuePolicy(t *testing.T) {
	t.Setenv(EnvQueuePolicy, "explode")
	if _, err := Load(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "queue_policy") {
		t.Errorf("expected queue_policy error, got %v", err)
	}
}
