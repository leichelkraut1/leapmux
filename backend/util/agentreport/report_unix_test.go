//go:build unix

package agentreport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportsRejectSymlinksAndPublicFiles(t *testing.T) {
	t.Parallel()
	dir := reportDirectory(t)
	now := time.Now().UTC()
	report := fixtureReport(now)
	outside := filepath.Join(filepath.Dir(dir), "outside.json")
	putReport(t, filepath.Dir(dir), "outside.json", report)
	link := filepath.Join(dir, reportName(report.SessionID))
	require.NoError(t, os.Symlink(outside, link))
	rows, err := Read(context.Background(), dir, now)
	require.NoError(t, err)
	assert.Empty(t, rows)
	require.ErrorIs(t, Write(dir, report), ErrUnavailable)
	require.NoError(t, os.Remove(link))
	putReport(t, dir, reportName(report.SessionID), report)
	require.NoError(t, os.Chmod(link, 0o644))
	rows, err = Read(context.Background(), dir, now)
	require.NoError(t, err)
	assert.Empty(t, rows)
	require.ErrorIs(t, Write(dir, report), ErrUnavailable)

	alias := filepath.Join(filepath.Dir(dir), "alias")
	require.NoError(t, os.Symlink(dir, alias))
	_, err = Read(context.Background(), alias, now)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, Write(alias, report), ErrUnavailable)
	_, err = Read(context.Background(), filepath.Join(alias, "missing"), now)
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestWriteMakesExistingOwnedDirectoryPrivate(t *testing.T) {
	t.Parallel()
	dir := reportDirectory(t)
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, Write(dir, fixtureReport(time.Now().UTC())))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
