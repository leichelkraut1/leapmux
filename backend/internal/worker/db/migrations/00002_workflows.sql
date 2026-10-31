-- +goose Up

-- Workflows arrive as their own migration, not as an edit to 00001, because
-- this fork runs deployed Workers whose databases already recorded 00001 and
-- hold chats that must survive the upgrade: goose never re-runs a recorded
-- migration, so tables added to 00001 would never reach them.

-- Workflows (proto/leapmux/v1/workflow.proto). A worker holds two halves:
--
--   * The LIBRARY (profiles, templates) of the user who saved them here.
--   * The RUNS it coordinates -- every run whose Lead agent it hosts -- and
--     the bindings of the workflow agents it hosts for any coordinator.
--
-- Definitions are stored as proto blobs because their shape is the protocol's
-- and nothing filters on their inner fields. The state the engine filters and
-- transitions on is columns. Times are unix milliseconds, as the protocol
-- carries them. Every enum column stores its proto ordinal under a CHECK from
-- 1 (enum_column_numbering_test.go pins each range).
CREATE TABLE workflow_profiles (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL CHECK (user_id <> ''),
    name          TEXT NOT NULL,
    spec          BLOB NOT NULL,
    updated_at_ms INTEGER NOT NULL
);
CREATE INDEX idx_workflow_profiles_user ON workflow_profiles(user_id);

CREATE TABLE workflow_templates (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL CHECK (user_id <> ''),
    name          TEXT NOT NULL,
    version       INTEGER NOT NULL CHECK (version >= 1),
    spec          BLOB NOT NULL,
    updated_at_ms INTEGER NOT NULL
);
CREATE INDEX idx_workflow_templates_user ON workflow_templates(user_id);

-- A run this worker coordinates. template is the WorkflowTemplate snapshot
-- the run was launched from, so a later template edit never reaches it.
CREATE TABLE workflow_runs (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL CHECK (user_id <> ''),
    name             TEXT NOT NULL,
    template_id      TEXT NOT NULL DEFAULT '',
    template_version INTEGER NOT NULL DEFAULT 0,
    template         BLOB NOT NULL,
    status           INTEGER NOT NULL
        CHECK (typeof(status) = 'integer') CHECK (status BETWEEN 1 AND 8),
    status_detail    TEXT NOT NULL DEFAULT '',
    objective        TEXT NOT NULL,
    working_dir      TEXT NOT NULL,
    base_branch      TEXT NOT NULL DEFAULT '',
    workspace_id     TEXT NOT NULL,
    lead_slot_id     TEXT NOT NULL DEFAULT '',
    lead_agent_id    TEXT NOT NULL DEFAULT '',
    summary          TEXT NOT NULL DEFAULT '',
    created_at_ms    INTEGER NOT NULL,
    started_at_ms    INTEGER NOT NULL DEFAULT 0,
    completed_at_ms  INTEGER NOT NULL DEFAULT 0,
    updated_at_ms    INTEGER NOT NULL
);
CREATE INDEX idx_workflow_runs_user ON workflow_runs(user_id, created_at_ms);

-- The resolved role (WorkflowRole) and the computer it is placed on. For a
-- SPECIFIC_WORKER role worker_id is the pinned computer and changes only when
-- the user reassigns it.
CREATE TABLE workflow_run_roles (
    run_id    TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    role_key  TEXT NOT NULL,
    role      BLOB NOT NULL,
    worker_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, role_key)
);

-- One participating agent. The slot is reserved (STARTING, no agent_id)
-- before the agent is opened, so a coordinator that restarts mid-spawn finds
-- the slot and asks the hosting worker (FindWorkflowAgents) rather than
-- opening a second agent. token authenticates the agent's calls. It is kept
-- raw because a re-open after a restart must hand the same credential to the
-- agent; the hosting worker's binding holds the same value.
CREATE TABLE workflow_run_agents (
    slot_id         TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    role_key        TEXT NOT NULL,
    agent_id        TEXT NOT NULL DEFAULT '',
    worker_id       TEXT NOT NULL,
    status          INTEGER NOT NULL
        CHECK (typeof(status) = 'integer') CHECK (status BETWEEN 1 AND 5),
    status_detail   TEXT NOT NULL DEFAULT '',
    token           TEXT NOT NULL UNIQUE CHECK (token <> ''),
    current_task_id TEXT NOT NULL DEFAULT '',
    created_at_ms   INTEGER NOT NULL,
    updated_at_ms   INTEGER NOT NULL
);
CREATE INDEX idx_workflow_run_agents_run ON workflow_run_agents(run_id);

CREATE TABLE workflow_tasks (
    id              TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    step_key        TEXT NOT NULL DEFAULT '',
    role_key        TEXT NOT NULL,
    title           TEXT NOT NULL,
    instructions    TEXT NOT NULL DEFAULT '',
    status          INTEGER NOT NULL
        CHECK (typeof(status) = 'integer') CHECK (status BETWEEN 1 AND 10),
    status_detail   TEXT NOT NULL DEFAULT '',
    approval        INTEGER NOT NULL
        CHECK (typeof(approval) = 'integer') CHECK (approval BETWEEN 1 AND 3),
    attempt         INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL CHECK (max_attempts >= 1),
    slot_id         TEXT NOT NULL DEFAULT '',
    worker_id       TEXT NOT NULL DEFAULT '',
    result          BLOB,
    review_feedback TEXT NOT NULL DEFAULT '',
    created_by      TEXT NOT NULL DEFAULT '',
    created_at_ms   INTEGER NOT NULL,
    started_at_ms   INTEGER NOT NULL DEFAULT 0,
    completed_at_ms INTEGER NOT NULL DEFAULT 0,
    updated_at_ms   INTEGER NOT NULL,
    UNIQUE (run_id, seq)
);

CREATE TABLE workflow_task_deps (
    task_id    TEXT NOT NULL REFERENCES workflow_tasks(id) ON DELETE CASCADE,
    depends_on TEXT NOT NULL REFERENCES workflow_tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on),
    CHECK (task_id <> depends_on)
);

CREATE TABLE workflow_approvals (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    task_id       TEXT NOT NULL DEFAULT '',
    kind          INTEGER NOT NULL
        CHECK (typeof(kind) = 'integer') CHECK (kind BETWEEN 1 AND 5),
    status        INTEGER NOT NULL
        CHECK (typeof(status) = 'integer') CHECK (status BETWEEN 1 AND 4),
    title         TEXT NOT NULL,
    details       TEXT NOT NULL DEFAULT '',
    requested_by  TEXT NOT NULL DEFAULT '',
    decision_note TEXT NOT NULL DEFAULT '',
    created_at_ms INTEGER NOT NULL,
    decided_at_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_workflow_approvals_run ON workflow_approvals(run_id);

-- The audit trail: delegation, results, reviews, approvals, user actions.
CREATE TABLE workflow_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL DEFAULT '',
    kind    INTEGER NOT NULL
        CHECK (typeof(kind) = 'integer') CHECK (kind BETWEEN 1 AND 10),
    actor   TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    at_ms   INTEGER NOT NULL
);
CREATE INDEX idx_workflow_events_run ON workflow_events(run_id, id);

-- Every message the engine sends an agent, recorded BEFORE it is sent. id is
-- the EnqueueAgentInput input id, which the hosting worker deduplicates, so a
-- resend after a restart or an unreachable worker delivers at most once.
-- task_id is set for a task's dispatch message, whose delivery is what moves
-- the task from DISPATCHING to RUNNING. delivered_at_ms is 0 while pending,
-- the delivery time once sent, and -1 for a message dropped because its agent
-- or run is gone.
CREATE TABLE workflow_outbox (
    id              TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    slot_id         TEXT NOT NULL,
    task_id         TEXT NOT NULL DEFAULT '',
    text            TEXT NOT NULL,
    delivered_at_ms INTEGER NOT NULL DEFAULT 0,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    created_at_ms   INTEGER NOT NULL
);
CREATE INDEX idx_workflow_outbox_pending ON workflow_outbox(run_id, delivered_at_ms, created_at_ms);

-- The hosting half: an agent on THIS worker that belongs to a run, possibly
-- coordinated elsewhere. Read at every spawn and relaunch, so the agent keeps
-- its LEAPMUX_WORKFLOW_* credential -- and, when confine is 1, its confined
-- control socket -- across a resume.
CREATE TABLE workflow_agent_bindings (
    agent_id              TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    run_id                TEXT NOT NULL,
    slot_id               TEXT NOT NULL,
    role_key              TEXT NOT NULL,
    coordinator_worker_id TEXT NOT NULL,
    token                 TEXT NOT NULL,
    confine               INTEGER NOT NULL DEFAULT 0 CHECK (confine IN (0, 1))
);
CREATE INDEX idx_workflow_agent_bindings_run ON workflow_agent_bindings(run_id);

-- +goose Down
DROP TABLE IF EXISTS workflow_agent_bindings;
DROP TABLE IF EXISTS workflow_outbox;
DROP TABLE IF EXISTS workflow_events;
DROP TABLE IF EXISTS workflow_approvals;
DROP TABLE IF EXISTS workflow_task_deps;
DROP TABLE IF EXISTS workflow_tasks;
DROP TABLE IF EXISTS workflow_run_agents;
DROP TABLE IF EXISTS workflow_run_roles;
DROP TABLE IF EXISTS workflow_runs;
DROP TABLE IF EXISTS workflow_templates;
DROP TABLE IF EXISTS workflow_profiles;
