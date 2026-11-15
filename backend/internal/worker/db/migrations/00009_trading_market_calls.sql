-- +goose Up

-- Every broker tool call a workflow agent makes through the trading lab's
-- read-only market tools (tradinglab.MarketCall): the audit trail of what
-- the research saw. A result is kept up to 512 KiB.
CREATE TABLE trading_market_calls (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id   TEXT NOT NULL CHECK (user_id <> ''),
    run_id    TEXT NOT NULL,
    task_id   TEXT NOT NULL,
    tool      TEXT NOT NULL,
    arguments TEXT NOT NULL,
    result    TEXT NOT NULL,
    is_error  INTEGER NOT NULL CHECK (is_error IN (0, 1)),
    at_ms     INTEGER NOT NULL
);
CREATE INDEX idx_trading_market_calls_task ON trading_market_calls(run_id, task_id);

-- +goose Down
DROP TABLE IF EXISTS trading_market_calls;
