package codexobserve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func candidateDatabase(t *testing.T, schema string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state_5.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(schema)
	require.NoError(t, err)
	return path, db
}

const metadataSchema = `CREATE TABLE threads(
 id TEXT PRIMARY KEY, name TEXT, archived INTEGER, updated_at_ms INTEGER, recency_at_ms INTEGER,
 preview TEXT, first_user_message TEXT, rollout_path TEXT)`

func TestReadCandidatesUsesOnlyExplicitNamesAndBoundedOrder(t *testing.T) {
	path, db := candidateDatabase(t, metadataSchema)
	for i := 0; i < 140; i++ {
		_, err := db.Exec("INSERT INTO threads VALUES(?,?,?,?,?,?,?,?)",
			fmt.Sprintf("%08x-1111-4111-8111-111111111111", i), "  Display\nname\u200b  ", 0, i, i,
			"PRIVATE PREVIEW", "PRIVATE MESSAGE", "PRIVATE PATH")
		require.NoError(t, err)
	}
	verifyCalls := 0
	result, err := readCandidatesVerified(context.Background(), path, func(string) error { verifyCalls++; return nil })
	require.NoError(t, err)
	require.Len(t, result.Candidates, 128)
	assert.True(t, result.Truncated)
	assert.Equal(t, 2, verifyCalls)
	assert.Equal(t, "0000008b-1111-4111-8111-111111111111", result.Candidates[0].ID)
	assert.Equal(t, "Display name", result.Candidates[0].Name)
	for _, row := range result.Candidates {
		assert.NotContains(t, row.Name, "PRIVATE")
	}
}
func TestReadCandidatesRejectsUnknownSchemaViewsUntrustedPathsAndCancellation(t *testing.T) {
	for name, schema := range map[string]string{
		"old schema":     "CREATE TABLE threads(id TEXT, title TEXT)",
		"view":           "CREATE VIEW threads AS SELECT 'id' AS id",
		"wrong affinity": "CREATE TABLE threads(id TEXT,name TEXT,archived TEXT,updated_at_ms INTEGER,recency_at_ms INTEGER)",
	} {
		t.Run(name, func(t *testing.T) {
			path, _ := candidateDatabase(t, schema)
			_, err := readCandidatesVerified(context.Background(), path, func(string) error { return nil })
			require.ErrorIs(t, err, errCandidates)
		})
	}
	path, _ := candidateDatabase(t, metadataSchema)
	_, err := readCandidatesVerified(context.Background(), path, func(string) error { return errors.New("private path") })
	require.ErrorIs(t, err, errCandidates)
	assert.NotContains(t, err.Error(), "private path")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = readCandidatesVerified(ctx, path, func(string) error { return nil })
	require.ErrorIs(t, err, errCandidates)
	missing := filepath.Join(t.TempDir(), "state_5.sqlite")
	_, err = readCandidatesVerified(context.Background(), missing, func(string) error { return nil })
	require.ErrorIs(t, err, errCandidates)
	_, err = os.Stat(missing)
	require.True(t, os.IsNotExist(err))
}
func TestReadCandidatesSkipsArchivedAndMalformedIDsWithoutInferringActivity(t *testing.T) {
	path, db := candidateDatabase(t, metadataSchema)
	for _, row := range []struct {
		id       string
		archived int
		name     any
	}{
		{testThread, 0, nil}, {testOwner, 1, "Archived"}, {"not a UUID", 0, "Invalid"},
	} {
		_, err := db.Exec("INSERT INTO threads VALUES(?,?,?,?,?,?,?,?)", row.id, row.name, row.archived, 0, 0, "secret", "secret", "secret")
		require.NoError(t, err)
	}
	result, err := readCandidatesVerified(context.Background(), path, func(string) error { return nil })
	require.NoError(t, err)
	require.Len(t, result.Candidates, 1)
	assert.Equal(t, candidate{ID: testThread}, result.Candidates[0])
	assert.True(t, result.Truncated)
}
func TestMetadataPathsUsesOnlyExplicitWorkerConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CODEX_SQLITE_HOME", "")
	codexHome, path, err := metadataPaths()
	require.NoError(t, err)
	assert.Equal(t, home, codexHome)
	assert.Equal(t, filepath.Join(home, "state_5.sqlite"), path)
	custom := t.TempDir()
	t.Setenv("CODEX_SQLITE_HOME", custom)
	_, path, err = metadataPaths()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(custom, "state_5.sqlite"), path)
	t.Setenv("CODEX_SQLITE_HOME", "relative")
	_, _, err = metadataPaths()
	require.ErrorIs(t, err, errCandidates)
}
