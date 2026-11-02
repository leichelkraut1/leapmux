//go:build linux

package procutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadProcShortNameNeverExpandsLongNamesFromArguments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "123")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "comm"), []byte("long-process-na\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cmdline"), []byte("different-program\x00private-argument\x00"), 0600))
	name, err := readProcShortName(context.Background(), root, 123)
	require.NoError(t, err)
	assert.Equal(t, "long-process-na", name)
	require.NoError(t, os.Remove(filepath.Join(dir, "comm")))
	name, err = readProcShortName(context.Background(), root, 123)
	require.ErrorIs(t, err, os.ErrNotExist, "missing comm never falls back to cmdline")
	assert.Empty(t, name)
}

func TestReadProcShortNameHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	name, err := readProcShortName(ctx, t.TempDir(), 123)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, name)
}

func TestReadProcUserKeyReadsOnlyEffectiveUIDMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "123")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tlong-process-na\nUid:\t1000\t1001\t1002\t1003\n"), 0600))
	// No cmdline or environ file exists: even a long Name must not trigger either.
	key, err := readProcUserKey(context.Background(), root, 123)
	require.NoError(t, err)
	assert.Equal(t, "1001", key)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tlong-process-na\n"), 0600))
	key, err = readProcUserKey(context.Background(), root, 123)
	require.Error(t, err)
	assert.Empty(t, key)
}
