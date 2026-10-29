package db_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

// A deployed Worker's database recorded 00001 long ago and holds chats. The
// upgrade must add the later migrations' tables and keep every existing row:
// goose never re-runs a recorded migration, which is why a schema addition
// ships as a migration of its own rather than as an edit to 00001.
func TestMigrateUpgradesADatabaseThatRecordedOnlyTheInitialMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := workerdb.Open(filepath.Join(t.TempDir(), "worker.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	initial, err := os.ReadFile(workerMigration)
	require.NoError(t, err)
	old, err := goose.NewProvider(goose.DialectSQLite3, conn, fstest.MapFS{"00001_initial.sql": {Data: initial}})
	require.NoError(t, err)
	_, err = old.Up(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO agents (id, title) VALUES ('kept', 'an existing chat')`)
	require.NoError(t, err)

	require.NoError(t, workerdb.Migrate(ctx, conn))

	var title string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT title FROM agents WHERE id = 'kept'`).Scan(&title))
	assert.Equal(t, "an existing chat", title, "the upgrade keeps existing chats")
	for _, table := range []string{"workflow_runs", "workflow_tasks", "workflow_outbox", "workflow_agent_bindings", "workflow_templates"} {
		var n int
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n))
		assert.Equalf(t, 1, n, "the upgrade adds %s", table)
	}

	require.NoError(t, workerdb.Migrate(ctx, conn), "a second start finds nothing to do")
}
