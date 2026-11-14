-- +goose Up

-- The trading lab's read-only Claude connector (internal/worker/labmcp): an
-- OAuth 2.1 server and MCP endpoint the worker serves only when configured.
-- No code, token or passphrase is stored in the clear: the passphrase as an
-- argon2id hash, codes and tokens as SHA-256 hashes.

-- One row per user: the connector passphrase, separate from the order
-- confirmation passphrase so signing in from a phone never types the one
-- that confirms orders.
CREATE TABLE trading_connector_auth (
    user_id         TEXT PRIMARY KEY CHECK (user_id <> ''),
    passphrase_hash TEXT NOT NULL,
    updated_at_ms   INTEGER NOT NULL
);

-- A dynamically registered OAuth client. redirect_uris is a JSON array.
CREATE TABLE trading_connector_clients (
    id            TEXT PRIMARY KEY CHECK (id <> ''),
    name          TEXT NOT NULL,
    redirect_uris TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL
);

-- Authorization codes, access tokens and refresh tokens by hash
-- (TradingConnectorTokenKind). A code keeps its redirect URI and PKCE
-- challenge; a token its client and user.
CREATE TABLE trading_connector_tokens (
    hash           TEXT PRIMARY KEY CHECK (hash <> ''),
    token_kind     INTEGER NOT NULL
        CHECK (typeof(token_kind) = 'integer') CHECK (token_kind BETWEEN 1 AND 3),
    client_id      TEXT NOT NULL,
    user_id        TEXT NOT NULL CHECK (user_id <> ''),
    redirect_uri   TEXT NOT NULL DEFAULT '',
    code_challenge TEXT NOT NULL DEFAULT '',
    expires_at_ms  INTEGER NOT NULL,
    created_at_ms  INTEGER NOT NULL
);
CREATE INDEX idx_trading_connector_tokens_expiry ON trading_connector_tokens(expires_at_ms);
CREATE INDEX idx_trading_connector_tokens_client ON trading_connector_tokens(client_id);

-- +goose Down
DROP TABLE IF EXISTS trading_connector_tokens;
DROP TABLE IF EXISTS trading_connector_clients;
DROP TABLE IF EXISTS trading_connector_auth;
