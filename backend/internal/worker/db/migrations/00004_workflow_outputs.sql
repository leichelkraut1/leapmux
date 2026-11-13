-- +goose Up

-- Its own migration for the reason 00002_workflows.sql states: deployed
-- Workers already recorded the earlier ones.

-- An accepted structured output (WorkflowFrozenOutput), written once when its
-- task succeeds. The triggers below refuse every UPDATE and DELETE, so what a
-- later step and the user read is exactly what was submitted, and the stored
-- SHA-256 can be checked against canonical_json at any time. There is no
-- foreign key to workflow_runs: a cascade would be a DELETE, and a run that
-- holds frozen outputs is never deleted (Engine.ControlRun refuses it).
CREATE TABLE workflow_frozen_outputs (
    run_id         TEXT NOT NULL,
    task_id        TEXT NOT NULL,
    step_key       TEXT NOT NULL,
    role_key       TEXT NOT NULL DEFAULT '',
    executor       TEXT NOT NULL DEFAULT '',
    sha256         TEXT NOT NULL CHECK (length(sha256) = 64),
    canonical_json TEXT NOT NULL CHECK (canonical_json <> ''),
    attempt        INTEGER NOT NULL CHECK (attempt >= 1),
    slot_id        TEXT NOT NULL DEFAULT '',
    frozen_at_ms   INTEGER NOT NULL,
    PRIMARY KEY (run_id, task_id)
);

-- +goose StatementBegin
CREATE TRIGGER workflow_frozen_outputs_no_update BEFORE UPDATE ON workflow_frozen_outputs
BEGIN
    SELECT RAISE(ABORT, 'workflow_frozen_outputs is immutable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER workflow_frozen_outputs_no_delete BEFORE DELETE ON workflow_frozen_outputs
BEGIN
    SELECT RAISE(ABORT, 'workflow_frozen_outputs is immutable');
END;
-- +goose StatementEnd

-- The label each anonymized input was shown under (WorkflowInputLabel). The
-- first dispatch of a step records its labels; a retry reads them back, so an
-- agent never sees one submission under two names. Only the user reads this.
CREATE TABLE workflow_input_labels (
    run_id          TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_key        TEXT NOT NULL,
    label           TEXT NOT NULL,
    source_step_key TEXT NOT NULL,
    source_task_id  TEXT NOT NULL,
    sha256          TEXT NOT NULL,
    PRIMARY KEY (run_id, step_key, label),
    UNIQUE (run_id, step_key, source_task_id)
);

-- +goose Down
DROP TABLE IF EXISTS workflow_input_labels;
DROP TRIGGER IF EXISTS workflow_frozen_outputs_no_delete;
DROP TRIGGER IF EXISTS workflow_frozen_outputs_no_update;
DROP TABLE IF EXISTS workflow_frozen_outputs;
