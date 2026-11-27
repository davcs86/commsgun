CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  role          TEXT NOT NULL CHECK (role = 'admin'),
  password_hash TEXT NOT NULL,
  disabled      INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE identities (
  user_id    TEXT NOT NULL REFERENCES users(id),
  issuer     TEXT NOT NULL,
  subject    TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (issuer, subject),
  UNIQUE (user_id, issuer)
);

CREATE TABLE sessions (
  token_hash      BLOB PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id),
  kind            TEXT NOT NULL CHECK (kind IN ('local', 'oidc')),
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL,
  reauth_at       INTEGER,
  reauth_failures INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX sessions_user ON sessions(user_id);
