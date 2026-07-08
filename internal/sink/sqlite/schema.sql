CREATE TABLE IF NOT EXISTS requests (
  id TEXT PRIMARY KEY,
  ts TEXT NOT NULL,
  endpoint TEXT,
  method TEXT,
  model TEXT,
  status INTEGER,
  latency_ms INTEGER,
  ttft_ms INTEGER,
  stream INTEGER,
  truncated INTEGER,
  system_bytes INTEGER,
  tools_bytes INTEGER,
  total_bytes INTEGER,
  message_count INTEGER,
  stop_reason TEXT,
  response_error TEXT,
  input_tokens INTEGER,
  output_tokens INTEGER,
  cache_read_tokens INTEGER,
  cache_creation_tokens INTEGER,
  cost_usd REAL,
  headers_json TEXT,
  raw_request BLOB,
  response_json TEXT
);

CREATE TABLE IF NOT EXISTS request_tools (
  request_id TEXT NOT NULL REFERENCES requests(id),
  name TEXT NOT NULL,
  bytes INTEGER,
  approx_tokens INTEGER
);

CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model);
CREATE INDEX IF NOT EXISTS idx_tools_req ON request_tools(request_id);
