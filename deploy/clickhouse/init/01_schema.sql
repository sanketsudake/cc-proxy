CREATE DATABASE IF NOT EXISTS claude;

CREATE TABLE IF NOT EXISTS claude.requests (
  id String,
  ts DateTime64(3),
  endpoint LowCardinality(String),
  method LowCardinality(String),
  model LowCardinality(String),
  status UInt16,
  latency_ms UInt32,
  ttft_ms UInt32,
  stream UInt8,
  truncated UInt8,
  session_id String,
  app LowCardinality(String),
  client_version LowCardinality(String),
  retry_count UInt8,
  account_id LowCardinality(String),
  device_id LowCardinality(String),
  system_bytes UInt32,
  tools_bytes UInt32,
  total_bytes UInt32,
  message_count UInt16,
  stop_reason LowCardinality(String),
  input_tokens UInt32,
  output_tokens UInt32,
  cache_read_tokens UInt32,
  cache_creation_tokens UInt32,
  cost_usd Float64,
  tools Nested(name String, bytes UInt32)
)
ENGINE = MergeTree
ORDER BY (ts, model)
PARTITION BY toYYYYMM(ts)
TTL toDateTime(ts) + INTERVAL 90 DAY;
