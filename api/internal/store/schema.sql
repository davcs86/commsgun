CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version(version) VALUES (1);

CREATE TABLE requests (
  id              TEXT PRIMARY KEY,
  app             TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  body_hash       BLOB NOT NULL,
  created_at      INTEGER NOT NULL,
  UNIQUE (app, idempotency_key)
);

CREATE TABLE messages (
  id                  TEXT PRIMARY KEY,
  request_id          TEXT NOT NULL REFERENCES requests(id),
  app                 TEXT NOT NULL,
  channel             TEXT NOT NULL,
  recipient           TEXT NOT NULL,
  sender              TEXT NOT NULL,
  state               TEXT NOT NULL,
  hold_reason         TEXT,
  attempts            INTEGER NOT NULL DEFAULT 0,
  error_type          TEXT,
  provider            TEXT,
  provider_message_id TEXT,
  subject             TEXT,
  body_text           TEXT,
  body_html           TEXT,
  reply_to            TEXT,
  sms_body            TEXT,
  body_bytes          INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL,
  sent_at             INTEGER,
  final_at            INTEGER,
  expires_at          INTEGER NOT NULL,
  next_attempt_at     INTEGER,
  locked_until        INTEGER,
  next_poll_at        INTEGER,
  poll_count          INTEGER NOT NULL DEFAULT 0,
  source_ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX messages_due ON messages(state, next_attempt_at);
CREATE INDEX messages_created ON messages(created_at DESC);
CREATE INDEX messages_request ON messages(request_id);

CREATE TABLE attempts (
  message_id      TEXT NOT NULL REFERENCES messages(id),
  n               INTEGER NOT NULL,
  started_at      INTEGER NOT NULL,
  duration_ms     INTEGER NOT NULL,
  outcome         TEXT NOT NULL,
  error_type      TEXT,
  provider_status INTEGER,
  provider_error  TEXT,
  message         TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (message_id, n)
);

CREATE TABLE rate_counters (
  scope        TEXT NOT NULL,
  window_kind  TEXT NOT NULL,
  window_start INTEGER NOT NULL,
  count        INTEGER NOT NULL,
  PRIMARY KEY (scope, window_kind, window_start)
);

CREATE TABLE provider_pauses (
  provider TEXT PRIMARY KEY,
  until    INTEGER NOT NULL,
  reason   TEXT NOT NULL
);
