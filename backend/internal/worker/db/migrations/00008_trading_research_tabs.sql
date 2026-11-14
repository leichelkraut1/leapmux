-- +goose Up
-- Admits TAB_TYPE_TRADING_RESEARCH (6) to the tab payload registry: a
-- Trading research tab is a link to the lab of the worker that holds it, like
-- a native conversation tab, and owns no directory. SQLite cannot alter a
-- CHECK, so the table is rebuilt as 00003 rebuilt it, copying every row and
-- recreating its two dependent views inside Goose's migration transaction.
DROP VIEW worktree_tab_liveness;
DROP VIEW tab_locations;

CREATE TABLE worker_tab_payloads_new (
    -- CHECK (user_id <> ''): the worker's database has no users table, so the
    -- hub's REFERENCES users(id) floor cannot reach here. TabPayloadStore
    -- refuses a blank owner in Go, but a blank row that got in any other way
    -- would be permanently unclearable -- Get/RevokeRow both refuse an
    -- unminted owner, and the reconciler's scope check skips it -- while
    -- worktree_tab_liveness's payload branch (which matches p.user_id = t.user_id
    -- and relies on '' never matching a real payload row) would start reading
    -- agent/terminal links as live and leak their worktrees.
    user_id      TEXT NOT NULL CHECK (user_id <> ''),
    tab_id       TEXT NOT NULL,
    -- TabType: 3 = FILE, 4 = IMAGE, 5 = NATIVE_CONVERSATION, 6 =
    -- TRADING_RESEARCH. Stored
    -- rather than derived from the payload so tab_locations can project it
    -- without decoding a blob, and so a row whose payload the running binary
    -- cannot parse still reports which kind of tab it is.
    --
    -- The CHECK is the floor the Go enum cannot supply, and it is the same
    -- defence the user_id CHECK above states. Readers cast this column straight
    -- to leapmuxv1.TabType, and two of them -- orphan_reconciler's
    -- newOwnedTabKey and bootstrap's payload-tab filter -- have no default
    -- case, so an out-of-range row would be keyed against nothing in the hub's
    -- tab list and reaped as an orphan.
    tab_type     INTEGER NOT NULL CHECK (tab_type IN (3, 4, 5, 6)),
    -- The serialized leapmux.v1.TabPayload. Opaque here on purpose: the shared
    -- columns beside it are exactly what shared code reads, and the kind-
    -- specific half belongs to the kind. The CHECK enumerates supported kinds.
    payload      BLOB NOT NULL,
    -- The tab's git working directory, and the reason such a tab is a
    -- first-class git citizen rather than a type shared code routes around.
    -- agents.working_dir / terminals.working_dir are what every branch-context
    -- operation resolves a tab through -- last-tab close inspection, the
    -- sibling-on-this-branch scan, push -- and this column is this tab's entry
    -- in that same lookup (see getTabWorkingDir).
    --
    -- It is the ORIGINATING tab's working dir, forwarded by the client that
    -- opened this one, not something derived from the payload. A file (or an
    -- image) is opened from an agent or terminal tab, and the branch group the
    -- new tab renders in is that tab's; deriving a dir from a file path
    -- instead would answer for whatever repo the file happens to sit in, which
    -- is a different question and a different answer whenever the two diverge.
    -- Writers that have no originating tab (`leapmux control tab open
    -- --type=file`) fall back to the file's own directory, normalized once at
    -- write time in TabPayloadStore.Register so every reader can just read the
    -- column. It is duplicated out of the blob for that reason: SQL reads it.
    working_dir  TEXT NOT NULL DEFAULT '',
    -- Mirrors agents.workspace_archived and terminals.workspace_archived, and
    -- exists for one reason: such a tab is a live reference to its working
    -- directory, so the quake terminal there ends only when no such tab is
    -- left. An ARCHIVED tab is not a live reference -- nobody can reach the
    -- panel from it -- so the count needs the flag, not only the row.
    --
    -- The tab owns no process, so unlike the other two tables nothing here
    -- pauses or stops on the flag. It is read, never acted on.
    workspace_archived INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, tab_id)
);
INSERT INTO worker_tab_payloads_new (user_id, tab_id, tab_type, payload, working_dir, workspace_archived, created_at)
SELECT user_id, tab_id, tab_type, payload, working_dir, workspace_archived, created_at FROM worker_tab_payloads;
DROP TABLE worker_tab_payloads;
ALTER TABLE worker_tab_payloads_new RENAME TO worker_tab_payloads;
CREATE INDEX idx_worker_tab_payloads_working_dir ON worker_tab_payloads(working_dir);

CREATE VIEW tab_locations AS
SELECT 1 AS tab_type, id AS tab_id, '' AS user_id, working_dir AS working_dir,
       workspace_archived AS workspace_archived, 0 AS is_quake
FROM agents WHERE closed_at IS NULL AND parent_agent_id IS NULL
UNION ALL
SELECT 2, id, '', working_dir, workspace_archived, is_quake
FROM terminals WHERE closed_at IS NULL
UNION ALL
SELECT tab_type, tab_id, user_id, working_dir, workspace_archived, 0
FROM worker_tab_payloads WHERE tab_type IN (3, 4);

CREATE VIEW worktree_tab_liveness AS
SELECT
    t.worktree_id AS worktree_id,
    t.tab_type AS tab_type,
    t.tab_id AS tab_id,
    t.user_id AS user_id,
    CASE WHEN
        EXISTS (SELECT 1 FROM tab_locations l
                WHERE l.tab_type = t.tab_type AND l.tab_id = t.tab_id AND l.user_id = t.user_id)
    THEN 1 ELSE 0 END AS is_live
FROM worktree_tabs t;



-- +goose Down
-- The copy refuses Trading research rows rather than discarding them: a
-- failed downgrade rolls back with every row intact.
DROP VIEW worktree_tab_liveness;
DROP VIEW tab_locations;

CREATE TABLE worker_tab_payloads_new (
    -- CHECK (user_id <> ''): the worker's database has no users table, so the
    -- hub's REFERENCES users(id) floor cannot reach here. TabPayloadStore
    -- refuses a blank owner in Go, but a blank row that got in any other way
    -- would be permanently unclearable -- Get/RevokeRow both refuse an
    -- unminted owner, and the reconciler's scope check skips it -- while
    -- worktree_tab_liveness's payload branch (which matches p.user_id = t.user_id
    -- and relies on '' never matching a real payload row) would start reading
    -- agent/terminal links as live and leak their worktrees.
    user_id      TEXT NOT NULL CHECK (user_id <> ''),
    tab_id       TEXT NOT NULL,
    -- TabType: 3 = FILE, 4 = IMAGE, 5 = NATIVE_CONVERSATION. Stored
    -- rather than derived from the payload so tab_locations can project it
    -- without decoding a blob, and so a row whose payload the running binary
    -- cannot parse still reports which kind of tab it is.
    --
    -- The CHECK is the floor the Go enum cannot supply, and it is the same
    -- defence the user_id CHECK above states. Readers cast this column straight
    -- to leapmuxv1.TabType, and two of them -- orphan_reconciler's
    -- newOwnedTabKey and bootstrap's payload-tab filter -- have no default
    -- case, so an out-of-range row would be keyed against nothing in the hub's
    -- tab list and reaped as an orphan.
    tab_type     INTEGER NOT NULL CHECK (tab_type IN (3, 4, 5)),
    -- The serialized leapmux.v1.TabPayload. Opaque here on purpose: the shared
    -- columns beside it are exactly what shared code reads, and the kind-
    -- specific half belongs to the kind. The CHECK enumerates supported kinds.
    payload      BLOB NOT NULL,
    -- The tab's git working directory, and the reason such a tab is a
    -- first-class git citizen rather than a type shared code routes around.
    -- agents.working_dir / terminals.working_dir are what every branch-context
    -- operation resolves a tab through -- last-tab close inspection, the
    -- sibling-on-this-branch scan, push -- and this column is this tab's entry
    -- in that same lookup (see getTabWorkingDir).
    --
    -- It is the ORIGINATING tab's working dir, forwarded by the client that
    -- opened this one, not something derived from the payload. A file (or an
    -- image) is opened from an agent or terminal tab, and the branch group the
    -- new tab renders in is that tab's; deriving a dir from a file path
    -- instead would answer for whatever repo the file happens to sit in, which
    -- is a different question and a different answer whenever the two diverge.
    -- Writers that have no originating tab (`leapmux control tab open
    -- --type=file`) fall back to the file's own directory, normalized once at
    -- write time in TabPayloadStore.Register so every reader can just read the
    -- column. It is duplicated out of the blob for that reason: SQL reads it.
    working_dir  TEXT NOT NULL DEFAULT '',
    -- Mirrors agents.workspace_archived and terminals.workspace_archived, and
    -- exists for one reason: such a tab is a live reference to its working
    -- directory, so the quake terminal there ends only when no such tab is
    -- left. An ARCHIVED tab is not a live reference -- nobody can reach the
    -- panel from it -- so the count needs the flag, not only the row.
    --
    -- The tab owns no process, so unlike the other two tables nothing here
    -- pauses or stops on the flag. It is read, never acted on.
    workspace_archived INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, tab_id)
);
INSERT INTO worker_tab_payloads_new (user_id, tab_id, tab_type, payload, working_dir, workspace_archived, created_at)
SELECT user_id, tab_id, tab_type, payload, working_dir, workspace_archived, created_at FROM worker_tab_payloads;
DROP TABLE worker_tab_payloads;
ALTER TABLE worker_tab_payloads_new RENAME TO worker_tab_payloads;
CREATE INDEX idx_worker_tab_payloads_working_dir ON worker_tab_payloads(working_dir);

CREATE VIEW tab_locations AS
SELECT 1 AS tab_type, id AS tab_id, '' AS user_id, working_dir AS working_dir,
       workspace_archived AS workspace_archived, 0 AS is_quake
FROM agents WHERE closed_at IS NULL AND parent_agent_id IS NULL
UNION ALL
SELECT 2, id, '', working_dir, workspace_archived, is_quake
FROM terminals WHERE closed_at IS NULL
UNION ALL
SELECT tab_type, tab_id, user_id, working_dir, workspace_archived, 0
FROM worker_tab_payloads WHERE tab_type IN (3, 4);

CREATE VIEW worktree_tab_liveness AS
SELECT
    t.worktree_id AS worktree_id,
    t.tab_type AS tab_type,
    t.tab_id AS tab_id,
    t.user_id AS user_id,
    CASE WHEN
        EXISTS (SELECT 1 FROM tab_locations l
                WHERE l.tab_type = t.tab_type AND l.tab_id = t.tab_id AND l.user_id = t.user_id)
    THEN 1 ELSE 0 END AS is_live
FROM worktree_tabs t;


