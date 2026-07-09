// Package config loads proxy configuration with precedence:
// flags > environment (CAP_*) > JSON config file > defaults.
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

	// DataDir is where file-based sinks live by default (markdown logs,
	// SQLite db). Defaults to ~/.cc-proxy; explicit sink paths override it.
	DataDir string `json:"data_dir"`

	QueueSize   int    `json:"queue_size"`   // per-sink buffered queue capacity
	QueuePolicy string `json:"queue_policy"` // drop|block

	MaxRequestBytes int64 `json:"max_request_bytes"` // request bodies above this are proxied but not captured
	MaxCaptureBytes int64 `json:"max_capture_bytes"` // response capture buffer cap

	Sinks   Sinks                   `json:"sinks"`
	Pricing map[string]ModelPricing `json:"pricing"` // model-prefix -> pricing override
}

// Defaults for every tunable. Markdown/SQLite locations default relative to
// DataDir and are resolved in Load, so a bare `cc-proxy` run never litters
// the current working directory.
const (
	DefaultPort        = 8787
	DefaultUpstreamURL = "https://api.anthropic.com"
	DefaultLogLevel    = "info"
	DefaultLogFormat   = "text"

	DefaultDataDirName = ".cc-proxy" // under $HOME
	DefaultLogsDirName = "logs"      // under DataDir
	DefaultDBFileName  = "cc-proxy.db"

	DefaultQueueSize       = 256
	DefaultMaxRequestBytes = 100 << 20 // 100 MB
	DefaultMaxCaptureBytes = 64 << 20  // 64 MB

	DefaultClickHouseURL      = "http://localhost:8123"
	DefaultClickHouseDatabase = "claude"
	DefaultClickHouseTable    = "requests"
	DefaultLokiURL            = "http://localhost:3100"
	DefaultBatchSize          = 50
	DefaultFlushInterval      = Duration(5 * time.Second)
	DefaultLokiMaxLineBytes   = 16 << 10
)

// Queue full-queue policies.
const (
	QueuePolicyDrop  = "drop"
	QueuePolicyBlock = "block"
)

// Environment variable names (all config is also reachable via flags and the
// JSON config file; env sits between them in precedence).
const (
	EnvConfig             = "CAP_CONFIG"
	EnvPort               = "CAP_PORT"
	EnvUpstreamURL        = "CAP_UPSTREAM_URL"
	EnvLogLevel           = "CAP_LOG_LEVEL"
	EnvLogFormat          = "CAP_LOG_FORMAT"
	EnvQuiet              = "CAP_QUIET"
	EnvDataDir            = "CAP_DATA_DIR"
	EnvQueueSize          = "CAP_QUEUE_SIZE"
	EnvQueuePolicy        = "CAP_QUEUE_POLICY"
	EnvMarkdownEnabled    = "CAP_SINK_MARKDOWN_ENABLED"
	EnvMarkdownDir        = "CAP_SINK_MARKDOWN_DIR"
	EnvSQLiteEnabled      = "CAP_SINK_SQLITE_ENABLED"
	EnvSQLitePath         = "CAP_SINK_SQLITE_PATH"
	EnvClickHouseEnabled  = "CAP_SINK_CLICKHOUSE_ENABLED"
	EnvClickHouseURL      = "CAP_SINK_CLICKHOUSE_URL"
	EnvClickHouseUsername = "CAP_SINK_CLICKHOUSE_USERNAME"
	EnvClickHousePassword = "CAP_SINK_CLICKHOUSE_PASSWORD"
	EnvLokiEnabled        = "CAP_SINK_LOKI_ENABLED"
	EnvLokiURL            = "CAP_SINK_LOKI_URL"
)

func Default() Config {
	return Config{
		Port:            DefaultPort,
		UpstreamURL:     DefaultUpstreamURL,
		LogLevel:        DefaultLogLevel,
		LogFormat:       DefaultLogFormat,
		QueueSize:       DefaultQueueSize,
		QueuePolicy:     QueuePolicyDrop,
		MaxRequestBytes: DefaultMaxRequestBytes,
		MaxCaptureBytes: DefaultMaxCaptureBytes,
		Sinks: Sinks{
			// Dir/Path left empty here; Load resolves them under DataDir.
			Markdown: MarkdownSink{Enabled: true},
			SQLite:   SQLiteSink{Enabled: true},
			ClickHouse: ClickHouseSink{
				URL:           DefaultClickHouseURL,
				Database:      DefaultClickHouseDatabase,
				Table:         DefaultClickHouseTable,
				Username:      "claude",
				Password:      "claude",
				BatchSize:     DefaultBatchSize,
				FlushInterval: DefaultFlushInterval,
			},
			Loki: LokiSink{
				URL:           DefaultLokiURL,
				MaxLineBytes:  DefaultLokiMaxLineBytes,
				BatchSize:     DefaultBatchSize,
				FlushInterval: DefaultFlushInterval,
			},
		},
	}
}

// Load builds the config from defaults, an optional JSON file, CAP_* env vars,
// and command-line flags, in increasing precedence.
func Load(args []string, stderr io.Writer) (Config, error) {
	cfg := Default()

	fs := flag.NewFlagSet("cc-proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", envStr(EnvConfig, ""), "path to JSON config file")
	port := fs.Int("port", 0, fmt.Sprintf("listen port (default %d)", DefaultPort))
	upstream := fs.String("upstream", "", "upstream base URL (default "+DefaultUpstreamURL+")")
	logLevel := fs.String("log-level", "", "log level: debug|info|warn|error")
	logFormat := fs.String("log-format", "", "log format: text|json")
	quiet := fs.Bool("quiet", false, "suppress per-request terminal audit output")
	dataDir := fs.String("data-dir", "", "directory for captured data (default ~/"+DefaultDataDirName+")")
	logDir := fs.String("log-dir", "", "directory for markdown request logs (default <data-dir>/"+DefaultLogsDirName+")")
	dbPath := fs.String("db", "", "SQLite database path (default <data-dir>/"+DefaultDBFileName+")")
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
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}

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
		case "data-dir":
			cfg.DataDir = *dataDir
		case "log-dir":
			cfg.Sinks.Markdown.Dir = *logDir
		case "db":
			cfg.Sinks.SQLite.Path = *dbPath
		}
	})

	cfg.resolvePaths()
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// resolvePaths fills values that could not be defaulted statically: file-sink
// locations depend on DataDir (which any precedence layer may override), and
// batching knobs left at zero take the shared defaults so sinks never have to
// re-default them.
func (c *Config) resolvePaths() {
	if c.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "." // no home dir (unusual): fall back to cwd
		}
		c.DataDir = filepath.Join(home, DefaultDataDirName)
	}
	if c.Sinks.Markdown.Dir == "" {
		c.Sinks.Markdown.Dir = filepath.Join(c.DataDir, DefaultLogsDirName)
	}
	if c.Sinks.SQLite.Path == "" {
		c.Sinks.SQLite.Path = filepath.Join(c.DataDir, DefaultDBFileName)
	}

	for _, b := range []*struct {
		size     *int
		interval *Duration
	}{
		{&c.Sinks.ClickHouse.BatchSize, &c.Sinks.ClickHouse.FlushInterval},
		{&c.Sinks.Loki.BatchSize, &c.Sinks.Loki.FlushInterval},
	} {
		if *b.size <= 0 {
			*b.size = DefaultBatchSize
		}
		if *b.interval <= 0 {
			*b.interval = DefaultFlushInterval
		}
	}
	if c.Sinks.Loki.MaxLineBytes <= 0 {
		c.Sinks.Loki.MaxLineBytes = DefaultLokiMaxLineBytes
	}
}

// ErrVersionRequested signals that --version was passed.
var ErrVersionRequested = fmt.Errorf("version requested")

func (c Config) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d", c.Port)
	}
	if c.QueuePolicy != QueuePolicyDrop && c.QueuePolicy != QueuePolicyBlock {
		return fmt.Errorf("queue_policy must be %s or %s, got %q", QueuePolicyDrop, QueuePolicyBlock, c.QueuePolicy)
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

// applyEnv overlays CAP_* variables. Malformed values are errors, not silent
// fallbacks — the env tier should fail as loudly as flags and the config file.
func applyEnv(cfg *Config) error {
	var errs []error
	cfg.Port = envInt(EnvPort, cfg.Port, &errs)
	cfg.UpstreamURL = envStr(EnvUpstreamURL, cfg.UpstreamURL)
	cfg.LogLevel = envStr(EnvLogLevel, cfg.LogLevel)
	cfg.LogFormat = envStr(EnvLogFormat, cfg.LogFormat)
	cfg.Quiet = envBool(EnvQuiet, cfg.Quiet, &errs)
	cfg.DataDir = envStr(EnvDataDir, cfg.DataDir)
	cfg.QueueSize = envInt(EnvQueueSize, cfg.QueueSize, &errs)
	cfg.QueuePolicy = envStr(EnvQueuePolicy, cfg.QueuePolicy)

	cfg.Sinks.Markdown.Enabled = envBool(EnvMarkdownEnabled, cfg.Sinks.Markdown.Enabled, &errs)
	cfg.Sinks.Markdown.Dir = envStr(EnvMarkdownDir, cfg.Sinks.Markdown.Dir)
	cfg.Sinks.SQLite.Enabled = envBool(EnvSQLiteEnabled, cfg.Sinks.SQLite.Enabled, &errs)
	cfg.Sinks.SQLite.Path = envStr(EnvSQLitePath, cfg.Sinks.SQLite.Path)
	cfg.Sinks.ClickHouse.Enabled = envBool(EnvClickHouseEnabled, cfg.Sinks.ClickHouse.Enabled, &errs)
	cfg.Sinks.ClickHouse.URL = envStr(EnvClickHouseURL, cfg.Sinks.ClickHouse.URL)
	cfg.Sinks.ClickHouse.Username = envStr(EnvClickHouseUsername, cfg.Sinks.ClickHouse.Username)
	cfg.Sinks.ClickHouse.Password = envStr(EnvClickHousePassword, cfg.Sinks.ClickHouse.Password)
	cfg.Sinks.Loki.Enabled = envBool(EnvLokiEnabled, cfg.Sinks.Loki.Enabled, &errs)
	cfg.Sinks.Loki.URL = envStr(EnvLokiURL, cfg.Sinks.Loki.URL)
	return errors.Join(errs...)
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int, errs *[]error) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q is not an integer", key, v))
		return def
	}
	return n
}

func envBool(key string, def bool, errs *[]error) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q is not a boolean", key, v))
		return def
	}
	return b
}
