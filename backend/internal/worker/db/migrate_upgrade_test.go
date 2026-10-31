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

// An older migration manifest must preserve existing chats and new workflow rows.
func TestWorkflowReleaseOldMigratorAgainstSchemaTwo(t *testing.T) {
	ctx := t.Context()
	conn, err := workerdb.Open(filepath.Join(t.TempDir(), "rollback-fixture.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	initial, err := os.ReadFile(workerMigration)
	require.NoError(t, err)
	oldFS := fstest.MapFS{"00001_initial.sql": {Data: initial}}
	old, err := goose.NewProvider(goose.DialectSQLite3, conn, oldFS)
	require.NoError(t, err)
	_, err = old.Up(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO agents (id, title) VALUES ('rollback-canary', 'generated harmless canary')`)
	require.NoError(t, err)
	require.NoError(t, workerdb.Migrate(ctx, conn))
	_, err = conn.ExecContext(ctx, `INSERT INTO workflow_profiles (id, user_id, name, spec, updated_at_ms) VALUES ('rollback-profile', 'fixture-owner', 'generated fixture', X'00', 1)`)
	require.NoError(t, err)
	var before int64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&before))
	require.EqualValues(t, 2, before)
	// A fresh provider avoids reusing the older provider's cached state. This
	// matches an old executable opening an already upgraded database.
	oldAgain, createErr := goose.NewProvider(goose.DialectSQLite3, conn, oldFS)
	oldErr := createErr
	if oldErr == nil {
		_, oldErr = oldAgain.Up(ctx)
	}
	t.Logf("OLD_MIGRATOR_SCHEMA2_ACCEPTED=%t", oldErr == nil)
	if oldErr != nil {
		t.Logf("Generated-fixture older-migrator refusal: %v", oldErr)
	}
	var after int64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&after))
	assert.Equal(t, before, after, "old startup must not downgrade or rewrite migration state")
	var title string
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT title FROM agents WHERE id = 'rollback-canary'`).Scan(&title))
	assert.Equal(t, "generated harmless canary", title)
	var profiles int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_profiles WHERE id = 'rollback-profile'`).Scan(&profiles))
	assert.Equal(t, 1, profiles, "old startup must not delete new workflow rows")
}
