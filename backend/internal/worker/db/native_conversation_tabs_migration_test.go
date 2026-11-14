package db_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNativeConversationTabsUpgradePreservesPrivateRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := workerdb.Open(filepath.Join(t.TempDir(), "worker.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	initial, err := os.ReadFile(workerMigration)
	require.NoError(t, err)
	workflows, err := os.ReadFile("migrations/00002_workflows.sql")
	require.NoError(t, err)
	// Model a deployed schema-two database: the recorded initial migration has
	// the original CHECK and lifecycle view, regardless of fresh-schema edits.
	oldInitial := strings.ReplaceAll(string(initial), "tab_type IN (3, 4, 5)", "tab_type IN (3, 4)")
	oldInitial = strings.ReplaceAll(oldInitial, "FROM worker_tab_payloads WHERE tab_type IN (3, 4);", "FROM worker_tab_payloads;")
	old, err := goose.NewProvider(goose.DialectSQLite3, conn, fstest.MapFS{
		"00001_initial.sql":   {Data: []byte(oldInitial)},
		"00002_workflows.sql": {Data: workflows},
	})
	require.NoError(t, err)
	_, err = old.Up(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO agents (id, title) VALUES ('kept-chat', 'generated chat canary')`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO worktrees (id, worktree_path, repo_root, branch_name) VALUES ('kept-tree', '/generated/tree', '/generated', 'fixture')`)
	require.NoError(t, err)
	for _, row := range []struct {
		owner, id, dir string
		kind, archived int
		blob           []byte
	}{
		{"owner-a", "same-tab-id", "/generated/file", 3, 0, []byte{0, 1, 0xff, 2}},
		{"owner-b", "same-tab-id", "/generated/image", 4, 1, []byte{3, 0, 0xfe, 4}},
	} {
		_, err = conn.ExecContext(ctx, `INSERT INTO worker_tab_payloads (user_id, tab_id, tab_type, payload, working_dir, workspace_archived, created_at) VALUES (?, ?, ?, ?, ?, ?, '2026-01-02T03:04:05.678Z')`, row.owner, row.id, row.kind, row.blob, row.dir, row.archived)
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, `INSERT INTO worktree_tabs (worktree_id, tab_type, tab_id, user_id) VALUES ('kept-tree', ?, ?, ?)`, row.kind, row.id, row.owner)
		require.NoError(t, err)
	}
	// A registry rebuild must not cascade into other tables. Pin that premise
	// against every table in the generated deployed schema before migrating.
	tables, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND sql LIKE '%REFERENCES worker_tab_payloads%'`)
	require.NoError(t, err)
	assert.False(t, tables.Next(), "no foreign key references the registry")
	require.NoError(t, tables.Close())
	_, err = conn.ExecContext(ctx, `INSERT INTO worker_tab_payloads (user_id, tab_id, tab_type, payload) VALUES ('owner-a', 'too-new', 5, X'00')`)
	require.Error(t, err, "the deployed schema must reject the new kind before its upgrade")

	require.NoError(t, workerdb.Migrate(ctx, conn))
	for _, owner := range []string{"owner-a", "owner-b"} {
		var kind, archived int
		var blob []byte
		var dir, created string
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT tab_type, payload, working_dir, workspace_archived, CAST(created_at AS TEXT) FROM worker_tab_payloads WHERE user_id = ? AND tab_id = 'same-tab-id'`, owner).Scan(&kind, &blob, &dir, &archived, &created))
		if owner == "owner-a" {
			assert.Equal(t, 3, kind)
			assert.Equal(t, []byte{0, 1, 0xff, 2}, blob)
			assert.Equal(t, "/generated/file", dir)
			assert.Equal(t, 0, archived)
		} else {
			assert.Equal(t, 4, kind)
			assert.Equal(t, []byte{3, 0, 0xfe, 4}, blob)
			assert.Equal(t, "/generated/image", dir)
			assert.Equal(t, 1, archived)
		}
		assert.Equal(t, "2026-01-02T03:04:05.678Z", created)
	}
	var n int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktree_tab_liveness WHERE is_live = 1`).Scan(&n))
	assert.Equal(t, 2, n, "existing file/image lifecycle links survive")
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_worker_tab_payloads_working_dir'`).Scan(&n))
	assert.Equal(t, 1, n)
	var title string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT title FROM agents WHERE id = 'kept-chat'`).Scan(&title))
	assert.Equal(t, "generated chat canary", title)

	payload := &leapmuxv1.TabPayload{WorkingDir: "/generated/destination", Kind: &leapmuxv1.TabPayload_NativeConversation{NativeConversation: &leapmuxv1.NativeConversationTabPayload{
		SourceWorkerId: "source-mac", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, SessionId: "generated-native-chat", DisplayName: "Generated chat",
	}}}
	blob, err := proto.Marshal(payload)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO worker_tab_payloads (user_id, tab_id, tab_type, payload, working_dir) VALUES ('owner-a', 'native-link', 5, ?, '/generated/destination')`, blob)
	require.NoError(t, err)
	var stored []byte
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT payload FROM worker_tab_payloads WHERE user_id = 'owner-a' AND tab_id = 'native-link'`).Scan(&stored))
	var decoded leapmuxv1.TabPayload
	require.NoError(t, proto.Unmarshal(stored, &decoded))
	assert.True(t, proto.Equal(payload, &decoded))
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tab_locations WHERE tab_type = 5`).Scan(&n))
	assert.Zero(t, n, "native placement never claims physical lifecycle ownership")
	_, err = conn.ExecContext(ctx, `INSERT INTO worker_tab_payloads (user_id, tab_id, tab_type, payload) VALUES ('owner-a', 'unsupported', 7, X'00')`)
	require.Error(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO worker_tab_payloads (user_id, tab_id, tab_type, payload) VALUES ('', 'blank-owner', 5, X'00')`)
	require.Error(t, err)
	require.NoError(t, workerdb.Migrate(ctx, conn), "restart is idempotent")
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, os.DirFS("migrations"))
	require.NoError(t, err)
	// Down to before 00003: the later migrations roll back, then 00003 refuses.
	_, err = provider.DownTo(ctx, 2)
	require.Error(t, err, "downgrading must refuse rather than discard native links")
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM worker_tab_payloads`).Scan(&n))
	assert.Equal(t, 3, n, "failed downgrade rolled all rows back")
	require.NoError(t, workerdb.Migrate(ctx, conn))
}
