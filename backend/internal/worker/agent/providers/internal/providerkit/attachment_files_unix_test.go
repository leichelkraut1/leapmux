//go:build unix

package providerkit

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachmentPrivateDirectoryAndFileModes(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "attachments")
	require.NoError(t, mkdirPrivateAttachmentDir(dir))
	info, err := os.Lstat(dir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), info.Mode().Perm())
	require.NoError(t, checkPrivateAttachmentPath(dir, info))

	path := filepath.Join(dir, "document.bin")
	require.NoError(t, os.WriteFile(path, []byte{0, 1, 2}, 0o600))
	info, err = os.Lstat(path)
	require.NoError(t, err)
	require.NoError(t, checkPrivateAttachmentPath(path, info))

	require.NoError(t, os.Chmod(path, 0o644))
	info, err = os.Lstat(path)
	require.NoError(t, err)
	require.ErrorIs(t, checkPrivateAttachmentPath(path, info), errAttachmentStorageUnsafe)
	after, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o644), after.Mode().Perm(), "validation never repairs permissions")

	require.NoError(t, os.Chmod(path, 0o700))
	info, err = os.Lstat(path)
	require.NoError(t, err)
	require.ErrorIs(t, checkPrivateAttachmentPath(path, info), errAttachmentStorageUnsafe)

	require.NoError(t, os.Chmod(dir, 0o755))
	info, err = os.Lstat(dir)
	require.NoError(t, err)
	require.ErrorIs(t, checkPrivateAttachmentPath(dir, info), errAttachmentStorageUnsafe)
	after, err = os.Lstat(dir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o755), after.Mode().Perm())
}

type attachmentOwnerFixture struct {
	fs.FileInfo
	stat *syscall.Stat_t
}

func (f attachmentOwnerFixture) Sys() any { return f.stat }

func TestAttachmentPrivatePathRejectsForeignOwnerAndLinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	info, err := os.Lstat(dir)
	require.NoError(t, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	foreign := *stat
	foreign.Uid = uint32(os.Geteuid()) + 1
	require.ErrorIs(t, checkPrivateAttachmentPath(dir, attachmentOwnerFixture{FileInfo: info, stat: &foreign}), errAttachmentStorageUnsafe)
	require.ErrorIs(t, checkPrivateAttachmentPath(dir, attachmentOwnerFixture{FileInfo: info}), errAttachmentStorageUnsafe)

	link := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, os.Symlink(dir, link))
	linkedInfo, err := os.Lstat(link)
	require.NoError(t, err)
	require.ErrorIs(t, checkPrivateAttachmentPath(link, linkedInfo), errAttachmentStorageUnsafe)
	require.ErrorIs(t, checkPrivateAttachmentPath(dir, nil), errAttachmentStorageUnsafe)
}
