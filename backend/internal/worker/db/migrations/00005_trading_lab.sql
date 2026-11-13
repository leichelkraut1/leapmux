-- +goose Up

-- The blind trading research lab (internal/worker/tradinglab,
-- proto/leapmux/v1/trading_lab.proto), on the coordinator of its runs. Its
-- own migration for the reason 00002_workflows.sql states.
--
-- Simulated and live data never share a row: shadow positions carry a
-- simulated book (1-3) and live positions the LIVE book (4), and live fills
-- have a table of their own. Every enum column stores its proto ordinal under
-- a CHECK from 1 (enum_column_numbering_test.go pins each range).

-- One row per user: TradingLabSettings, and the argon2id hash of the
-- confirmation passphrase, which no RPC ever returns.
CREATE TABLE trading_lab_settings (
    user_id         TEXT PRIMARY KEY CHECK (user_id <> ''),
    spec            BLOB NOT NULL,
    passphrase_hash TEXT NOT NULL DEFAULT '',
    updated_at_ms   INTEGER NOT NULL
);

-- The last broker verification (TradingBrokerStatus).
CREATE TABLE trading_broker_checks (
    user_id       TEXT PRIMARY KEY CHECK (user_id <> ''),
    spec          BLOB NOT NULL,
    checked_at_ms INTEGER NOT NULL
);

-- A captured market snapshot, written once by the snapshot executor for its
-- task, so a re-run after a restart reuses it. Immutable like the workflow's
-- frozen outputs, which hold the same canonical JSON under the same hash.
CREATE TABLE trading_snapshots (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL CHECK (user_id <> ''),
    run_id         TEXT NOT NULL,
    task_id        TEXT NOT NULL UNIQUE,
    sha256         TEXT NOT NULL CHECK (length(sha256) = 64),
    as_of_ms       INTEGER NOT NULL,
    source         INTEGER NOT NULL
        CHECK (typeof(source) = 'integer') CHECK (source BETWEEN 1 AND 2),
    canonical_json TEXT NOT NULL,
    captured_at_ms INTEGER NOT NULL
);
CREATE INDEX idx_trading_snapshots_run ON trading_snapshots(run_id);

-- +goose StatementBegin
CREATE TRIGGER trading_snapshots_no_update BEFORE UPDATE ON trading_snapshots
BEGIN
    SELECT RAISE(ABORT, 'trading_snapshots is immutable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trading_snapshots_no_delete BEFORE DELETE ON trading_snapshots
BEGIN
    SELECT RAISE(ABORT, 'trading_snapshots is immutable');
END;
-- +goose StatementEnd

-- A run the record executor has booked, so a re-run books nothing twice.
CREATE TABLE trading_runs (
    run_id         TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL CHECK (user_id <> ''),
    snapshot_id    TEXT NOT NULL,
    view           BLOB NOT NULL,
    recorded_at_ms INTEGER NOT NULL
);

-- Every recommendation of every run, eligible or not, and every no-trade
-- (TradingCandidate). source is the TradingBook of its analyst (BLIND or
-- CONTRARIAN); status is a TradingCandidateStatus.
CREATE TABLE trading_candidates (
    id                TEXT PRIMARY KEY,
    user_id           TEXT NOT NULL CHECK (user_id <> ''),
    run_id            TEXT NOT NULL,
    source            INTEGER NOT NULL
        CHECK (typeof(source) = 'integer') CHECK (source BETWEEN 1 AND 2),
    recommendation_id TEXT NOT NULL,
    status            INTEGER NOT NULL
        CHECK (typeof(status) = 'integer') CHECK (status BETWEEN 1 AND 6),
    spec              BLOB NOT NULL,
    created_at_ms     INTEGER NOT NULL,
    updated_at_ms     INTEGER NOT NULL,
    UNIQUE (run_id, source, recommendation_id)
);
CREATE INDEX idx_trading_candidates_user ON trading_candidates(user_id, status);

-- A simulated (book 1-3) or live (book 4) position (TradingPosition).
CREATE TABLE trading_positions (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL CHECK (user_id <> ''),
    book          INTEGER NOT NULL
        CHECK (typeof(book) = 'integer') CHECK (book BETWEEN 1 AND 4),
    candidate_id  TEXT NOT NULL DEFAULT '',
    state         INTEGER NOT NULL
        CHECK (typeof(state) = 'integer') CHECK (state BETWEEN 1 AND 4),
    spec          BLOB NOT NULL,
    opened_at_ms  INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
);
-- A simulated position's id is derived from its candidate and book
-- (tradinglab.positionID), so the primary key already keeps one per idea and
-- book and a re-run books nothing twice.
CREATE INDEX idx_trading_positions_user ON trading_positions(user_id, book, state);

-- Every observation of a position. horizon_days is 0 for an ordinary mark.
CREATE TABLE trading_marks (
    position_id      TEXT NOT NULL REFERENCES trading_positions(id) ON DELETE CASCADE,
    at_ms            INTEGER NOT NULL,
    horizon_days     INTEGER NOT NULL DEFAULT 0,
    bid              REAL NOT NULL DEFAULT 0,
    underlying_price REAL NOT NULL DEFAULT 0,
    value            REAL NOT NULL DEFAULT 0,
    missing          INTEGER NOT NULL DEFAULT 0 CHECK (missing IN (0, 1)),
    PRIMARY KEY (position_id, at_ms, horizon_days)
);

-- An order through the execution gateway (TradingOrder). client_order_id is
-- the idempotency key sent to the broker; its UNIQUE is what makes a second
-- submission of one confirmation impossible.
CREATE TABLE trading_orders (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL CHECK (user_id <> ''),
    state           INTEGER NOT NULL
        CHECK (typeof(state) = 'integer') CHECK (state BETWEEN 1 AND 10),
    side            INTEGER NOT NULL
        CHECK (typeof(side) = 'integer') CHECK (side BETWEEN 1 AND 2),
    client_order_id TEXT NOT NULL UNIQUE CHECK (client_order_id <> ''),
    spec            BLOB NOT NULL,
    created_at_ms   INTEGER NOT NULL,
    updated_at_ms   INTEGER NOT NULL
);
CREATE INDEX idx_trading_orders_user ON trading_orders(user_id, state);

-- Real fills as the broker reported them. Never written by the simulator.
CREATE TABLE trading_live_fills (
    order_id      TEXT NOT NULL REFERENCES trading_orders(id),
    fill_key      TEXT NOT NULL,
    quantity      INTEGER NOT NULL CHECK (quantity > 0),
    price         REAL NOT NULL CHECK (price >= 0),
    fees          REAL NOT NULL DEFAULT 0,
    at_ms         INTEGER NOT NULL,
    PRIMARY KEY (order_id, fill_key)
);

-- A standing, precisely scoped execution policy (TradingExecutionPolicy).
CREATE TABLE trading_policies (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL CHECK (user_id <> ''),
    active        INTEGER NOT NULL CHECK (active IN (0, 1)),
    spec          BLOB NOT NULL,
    expires_at_ms INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS trading_policies;
DROP TABLE IF EXISTS trading_live_fills;
DROP TABLE IF EXISTS trading_orders;
DROP TABLE IF EXISTS trading_marks;
DROP TABLE IF EXISTS trading_positions;
DROP TABLE IF EXISTS trading_candidates;
DROP TABLE IF EXISTS trading_runs;
DROP TRIGGER IF EXISTS trading_snapshots_no_delete;
DROP TRIGGER IF EXISTS trading_snapshots_no_update;
DROP TABLE IF EXISTS trading_snapshots;
DROP TABLE IF EXISTS trading_broker_checks;
DROP TABLE IF EXISTS trading_lab_settings;
