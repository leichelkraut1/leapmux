//go:build unix

package providerkit

import (
	"io/fs"
	"os"
	"syscall"
)

func mkdirPrivateAttachmentDir(path string) error {
	return os.Mkdir(path, 0o700)
}

// Existing attachment paths are never chmodded: a broad or foreign path is
// refused instead of silently changing the user's permissions.
func checkPrivateAttachmentPath(_ string, info fs.FileInfo) error {
	if info == nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errAttachmentStorageUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return errAttachmentStorageUnsafe
	}
	// Staging data never grants executable mode bits, including to the owner.
	if !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
		return errAttachmentStorageUnsafe
	}
	return nil
}
