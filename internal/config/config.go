// Package config loads proxy configuration with precedence:
// flags > environment (CAP_*) > JSON config file > defaults.
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Duration wraps time.Duration so JSON config files can use strings like "5s".
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case float64:
		*d = Duration(time.Duration(x))
	case string:
		parsed, err := time.ParseDuration(x)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", x, err)
		}
		*d = Duration(parsed)
	default:
		return fmt.Errorf("invalid duration value %v", v)
	}
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// ModelPricing is USD per million tokens. CacheRead/CacheWrite default to
// 0.1x/1.25x of Input when zero.
type ModelPricing struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type MarkdownSink struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
}

type SQLiteSink struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

type ClickHouseSink struct {
	Enabled       bool     `json:"enabled"`
	URL           string   `json:"url"`
	Database      string   `json:"database"`
	Table         string   `json:"table"`
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	BatchSize     int      `json:"batch_size"`
	FlushInterval Duration `json:"flush_interval"`
}

type LokiSink struct {
	Enabled       bool     `json:"enabled"`
	URL           string   `json:"url"`
	MaxLineBytes  int      `json:"max_line_bytes"`
	BatchSize     int      `json:"batch_size"`
	FlushInterval Duration `json:"flush_interval"`
}

type Sinks struct {
	Markdown   MarkdownSink   `json:"markdown"`
	SQLite     SQLiteSink     `json:"sqlite"`
	ClickHouse ClickHouseSink `json:"clickhouse"`
	Loki       LokiSink       `json:"loki"`
}

type Config struct {
	Port        int    `json:"port"`
	UpstreamURL string `json:"upstream_url"`
	LogLevel    string `json:"log_level"`  // debug|info|warn|error
	LogFormat   string `json:"log_format"` // text|json
	Quiet       bool   `json:"quiet"`      // suppress terminal audit tables

	QueueSize   int    `json:"queue_size"`   // per-sink buffered queue capacity
	QueuePolicy string `json:"queue_policy"` // drop|block

	MaxRequestBytes int64 `json:"max_request_bytes"` // request bodies above this are proxied but not captured
	MaxCaptureBytes int64 `json:"max_capture_bytes"` // response capture buffer cap

	Sinks   Sinks                   `json:"sinks"`
	Pricing map[string]ModelPricing `json:"pricing"` // model-prefix -> pricing override
}

func Default() Config {
	return Config{
		Port:            8787,
		UpstreamURL:     "https://api.anthropic.com",
		LogLevel:        "info",
		LogFormat:       "text",
		QueueSize:       256,
		QueuePolicy:     "drop",
		MaxRequestBytes: 100 << 20, // 100 MB
		MaxCaptureBytes: 64 << 20,  // 64 MB
		Sinks: Sinks{
			Markdown: MarkdownSink{Enabled: true, Dir: "logs"},
			SQLite:   SQLiteSink{Enabled: true, Path: "claude-agent-proxy.db"},
			ClickHouse: ClickHouseSink{
				URL:           "http://localhost:8123",
				Database:      "claude",
				Table:         "requests",
				Username:      "claude",
				Password:      "claude",
				BatchSize:     50,
				FlushInterval: Duration(5 * time.Second),
			},
			Loki: LokiSink{
				URL:           "http://localhost:3100",
				MaxLineBytes:  16 << 10,
				BatchSize:     50,
				FlushInterval: Duration(5 * time.Second),
			},
		},
	}
}

// Load builds the config from defaults, an optional JSON file, CAP_* env vars,
// and command-line flags, in increasing precedence.
func Load(args []string, stderr io.Writer) (Config, error) {
	cfg := Default()

	fs := flag.NewFlagSet("claude-agent-proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", envStr("CAP_CONFIG", ""), "path to JSON config file")
	port := fs.Int("port", 0, "listen port (default 8787)")
	upstream := fs.String("upstream", "", "upstream base URL (default https://api.anthropic.com)")
	logLevel := fs.String("log-level", "", "log level: debug|info|warn|error")
	logFormat := fs.String("log-format", "", "log format: text|json")
	quiet := fs.Bool("quiet", false, "suppress per-request terminal audit output")
	logDir := fs.String("log-dir", "", "directory for markdown request logs")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if *showVersion {
		return cfg, ErrVersionRequested
	}

	if *configPath != "" {
		if err := loadFile(*configPath, &cfg); err != nil {
			return cfg, err
		}
	}
	applyEnv(&cfg)

	// Flags win last; only apply the ones explicitly set.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			cfg.Port = *port
		case "upstream":
			cfg.UpstreamURL = *upstream
		case "log-level":
			cfg.LogLevel = *logLevel
		case "log-format":
			cfg.LogFormat = *logFormat
		case "quiet":
			cfg.Quiet = *quiet
		case "log-dir":
			cfg.Sinks.Markdown.Dir = *logDir
		}
	})

	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ErrVersionRequested signals that --version was passed.
var ErrVersionRequested = fmt.Errorf("version requested")

func (c Config) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d", c.Port)
	}
	if c.QueuePolicy != "drop" && c.QueuePolicy != "block" {
		return fmt.Errorf("queue_policy must be drop or block, got %q", c.QueuePolicy)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log_level %q", c.LogLevel)
	}
	return nil
}

// SlogLevel maps the configured level to slog.
func (c Config) SlogLevel() slog.Level {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func loadFile(path string, cfg *Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

func applyEnv(cfg *Config) {
	cfg.Port = envInt("CAP_PORT", cfg.Port)
	cfg.UpstreamURL = envStr("CAP_UPSTREAM_URL", cfg.UpstreamURL)
	cfg.LogLevel = envStr("CAP_LOG_LEVEL", cfg.LogLevel)
	cfg.LogFormat = envStr("CAP_LOG_FORMAT", cfg.LogFormat)
	cfg.Quiet = envBool("CAP_QUIET", cfg.Quiet)
	cfg.QueueSize = envInt("CAP_QUEUE_SIZE", cfg.QueueSize)
	cfg.QueuePolicy = envStr("CAP_QUEUE_POLICY", cfg.QueuePolicy)

	cfg.Sinks.Markdown.Enabled = envBool("CAP_SINK_MARKDOWN_ENABLED", cfg.Sinks.Markdown.Enabled)
	cfg.Sinks.Markdown.Dir = envStr("CAP_SINK_MARKDOWN_DIR", cfg.Sinks.Markdown.Dir)
	cfg.Sinks.SQLite.Enabled = envBool("CAP_SINK_SQLITE_ENABLED", cfg.Sinks.SQLite.Enabled)
	cfg.Sinks.SQLite.Path = envStr("CAP_SINK_SQLITE_PATH", cfg.Sinks.SQLite.Path)
	cfg.Sinks.ClickHouse.Enabled = envBool("CAP_SINK_CLICKHOUSE_ENABLED", cfg.Sinks.ClickHouse.Enabled)
	cfg.Sinks.ClickHouse.URL = envStr("CAP_SINK_CLICKHOUSE_URL", cfg.Sinks.ClickHouse.URL)
	cfg.Sinks.ClickHouse.Username = envStr("CAP_SINK_CLICKHOUSE_USERNAME", cfg.Sinks.ClickHouse.Username)
	cfg.Sinks.ClickHouse.Password = envStr("CAP_SINK_CLICKHOUSE_PASSWORD", cfg.Sinks.ClickHouse.Password)
	cfg.Sinks.Loki.Enabled = envBool("CAP_SINK_LOKI_ENABLED", cfg.Sinks.Loki.Enabled)
	cfg.Sinks.Loki.URL = envStr("CAP_SINK_LOKI_URL", cfg.Sinks.Loki.URL)
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
