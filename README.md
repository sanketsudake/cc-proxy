# cc-proxy

[![Go Reference](https://pkg.go.dev/badge/github.com/sanketsudake/cc-proxy.svg)](https://pkg.go.dev/github.com/sanketsudake/cc-proxy)
[![CI](https://github.com/sanketsudake/cc-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/sanketsudake/cc-proxy/actions/workflows/ci.yml)
[![CodeQL](https://github.com/sanketsudake/cc-proxy/actions/workflows/codeql.yml/badge.svg)](https://github.com/sanketsudake/cc-proxy/actions/workflows/codeql.yml)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/sanketsudake/cc-proxy)](go.mod)

See what Claude Code actually sends the model — and analyze it however you like.

A transparent logging proxy in Go that sits between the Claude Code CLI and the Anthropic API.
It forwards every request untouched (auth headers and all), streams the SSE response back with zero added latency, and fans a normalized capture record out to pluggable sinks: terminal audit tables, per-request Markdown documents, SQLite, ClickHouse, and Loki.
A bundled docker-compose stack gives you Grafana dashboards over your token usage, tool bloat, cache hit rate, latency, and estimated cost — all local.

Inspired by [Matt Pocock's agent-proxy gist](https://gist.github.com/mattpocock/5b3d76ea21f5f698aefded47a9cea3b1); the Markdown output is format-compatible with it.

## Quick start

Install via Homebrew (macOS):

```sh
brew install --cask sanketsudake/tap/cc-proxy
```

Or build from source:

```sh
make build
./cc-proxy
```

Point Claude Code at it in another terminal:

```sh
ANTHROPIC_BASE_URL=http://localhost:8787 claude
```

Every `/v1/messages` request now prints a ranked audit table to the proxy's terminal and writes a readable `.md` + raw `.request.txt` pair under `~/.cc-proxy/logs/`, plus a row in `~/.cc-proxy/cc-proxy.db` (SQLite).
`count_tokens` housekeeping calls pass through unlogged.

## The analytics stack (optional)

```sh
make up          # ClickHouse + Loki + Grafana via docker compose
CAP_SINK_CLICKHOUSE_ENABLED=true CAP_SINK_LOKI_ENABLED=true ./cc-proxy
```

Open [http://localhost:3000](http://localhost:3000) (anonymous admin, no login) — three dashboards are provisioned under the *Claude Agent Proxy* folder:

- **Token Usage** — stacked input/output/cache tokens over time, tokens by model, requests/min.
- **Tools & Cache** — top tools by bytes (your cut list), cache hit rate, request byte composition.
- **Latency & Cost** — p50/p95 latency and time-to-first-token, cost/hour per model, stop reasons, Loki log drill-down.

The proxy works fine with the stack down; it just warns (rate-limited to once per minute per sink) and drops the batch after retries.
`make down` stops the stack.

## Architecture

```
Claude Code ──HTTP──> proxy (:8787) ──HTTPS──> api.anthropic.com
                        │
                        ├── stream response to client (flushed per write, never buffered)
                        └── tee bytes ──> async capture pipeline ──> Dispatcher
                                          (audit + SSE decode + cost)   │
                                    ┌────────┬──────────┬────────┬─────┴────┐
                                 terminal  markdown   sqlite  clickhouse  loki
```

Every capture is attributed to its Claude Code session: the proxy records `X-Claude-Code-Session-Id`, `X-App`, the client version (`User-Agent`), and `X-Stainless-Retry-Count`, so concurrent sessions from different terminals stay distinguishable in every sink.
The hot path only appends bytes to a memory buffer; all parsing and sink I/O happens in worker goroutines after the response completes.
Each sink gets its own buffered queue and goroutine, so a hung backend never stalls proxying or the other sinks.
Credential headers (`authorization`, `x-api-key`, `api-key`, `cookie`) are redacted before any record leaves the pipeline.

## Configuration

Precedence: flags > environment (`CAP_*`) > JSON config file (`--config config.json`) > defaults.

| Env var | Default | What it does |
| --- | --- | --- |
| `CAP_PORT` | `8787` | Listen port |
| `CAP_UPSTREAM_URL` | `https://api.anthropic.com` | Upstream base URL |
| `CAP_LOG_LEVEL` / `CAP_LOG_FORMAT` | `info` / `text` | slog level and format (`json` available) |
| `CAP_QUIET` | `false` | Suppress per-request terminal audit tables |
| `CAP_QUEUE_SIZE` / `CAP_QUEUE_POLICY` | `256` / `drop` | Per-sink queue capacity and full-queue policy (`drop` or `block`) |
| `CAP_DATA_DIR` | `~/.cc-proxy` | Base directory for captured data (markdown logs + SQLite db) |
| `CAP_SINK_MARKDOWN_ENABLED` / `CAP_SINK_MARKDOWN_DIR` | `true` / `<data-dir>/logs` | Markdown sink |
| `CAP_SINK_SQLITE_ENABLED` / `CAP_SINK_SQLITE_PATH` | `true` / `<data-dir>/cc-proxy.db` | SQLite sink |
| `CAP_SINK_CLICKHOUSE_ENABLED` / `CAP_SINK_CLICKHOUSE_URL` | `false` / `http://localhost:8123` | ClickHouse sink (batch inserts) |
| `CAP_SINK_LOKI_ENABLED` / `CAP_SINK_LOKI_URL` | `false` / `http://localhost:3100` | Loki sink (push API) |

A JSON config file unlocks the rest (batch sizes, flush intervals, credentials, and per-model pricing overrides):

```json
{
  "sinks": {
    "clickhouse": {"enabled": true, "batch_size": 50, "flush_interval": "5s"}
  },
  "pricing": {
    "claude-sonnet-5": {"input": 3, "output": 15}
  }
}
```

Pricing is USD per million tokens, matched by model-ID prefix (longest wins); cache read defaults to 0.1× input and cache write to 1.25× input.
All cost figures are estimates — prices drift, override them when they do.

## Querying your data

**SQLite** — zero services needed:

```sh
sqlite3 ~/.cc-proxy/cc-proxy.db \
  "SELECT model, count(*), sum(input_tokens + cache_read_tokens), round(sum(cost_usd), 4)
   FROM requests GROUP BY model"

# Per-session breakdown (which terminal/session spent what)
sqlite3 ~/.cc-proxy/cc-proxy.db \
  "SELECT substr(session_id, 1, 8) AS session, count(*) AS reqs,
          sum(output_tokens) AS out_tokens, round(sum(cost_usd), 4) AS cost
   FROM requests WHERE session_id != '' GROUP BY session_id ORDER BY cost DESC"
```

**ClickHouse** — the analytics workhorse:

```sh
# What's eating my context? Average bytes per tool across all requests.
curl -s 'http://localhost:8123/' -H 'X-ClickHouse-User: claude' -H 'X-ClickHouse-Key: claude' \
  --data-binary "SELECT name, round(avg(bytes)) AS avg_bytes
                 FROM claude.requests ARRAY JOIN tools.name AS name, tools.bytes AS bytes
                 GROUP BY name ORDER BY avg_bytes DESC LIMIT 15"

# Cache hit rate by hour.
curl -s 'http://localhost:8123/' -H 'X-ClickHouse-User: claude' -H 'X-ClickHouse-Key: claude' \
  --data-binary "SELECT toStartOfHour(ts) AS h,
                        round(sum(cache_read_tokens) / greatest(sum(input_tokens + cache_read_tokens + cache_creation_tokens), 1), 3) AS hit_rate
                 FROM claude.requests GROUP BY h ORDER BY h"
```

**Loki (LogQL)** — grep-style over structured lines:

```logql
{job="cc-proxy"} | json | cost_usd > 0.05
{job="cc-proxy", model="claude-opus-4-8"} | json | stop_reason != "end_turn"
```

## Development

```sh
make test    # go test -race ./...
make lint    # golangci-lint run
```

The proxy test suite includes a real-time streaming proof: the client must receive the first SSE event while the upstream is still mid-stream.
Golden-file tests pin the Markdown output format (`go test ./internal/sink/markdown -update` to regenerate after intentional changes).
ClickHouse and Loki sinks are tested against httptest servers asserting wire shape, batching, and retry-then-drop behavior — no docker needed for `make test`.

## License

Released under the [MIT License](LICENSE).
