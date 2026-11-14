-- +goose Up

-- The trading lab's broker sign-in (internal/worker/tradinglab/brokerauth.go):
-- the OAuth client the worker registered with the broker's MCP server, and
-- the person's tokens, sealed for the worker's OS user (DPAPI on Windows).
-- One row per user; an empty access_sealed and refresh_sealed is signed out.
CREATE TABLE trading_broker_auth (
    user_id              TEXT PRIMARY KEY CHECK (user_id <> ''),
    server_url           TEXT NOT NULL,
    redirect_uri         TEXT NOT NULL,
    client_id            TEXT NOT NULL,
    issuer               TEXT NOT NULL,
    token_endpoint       TEXT NOT NULL,
    access_sealed        TEXT NOT NULL DEFAULT '',
    refresh_sealed       TEXT NOT NULL DEFAULT '',
    access_expires_at_ms INTEGER NOT NULL DEFAULT 0,
    signed_in_at_ms      INTEGER NOT NULL DEFAULT 0,
    updated_at_ms        INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS trading_broker_auth;
