//go:build unix

package agentreport

import (
	"io/fs"
	"os"
	"syscall"
)

func mkdirPrivate(dir string) error { return os.Mkdir(dir, 0o700) }

func owned(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func restrictDirectory(dir string, info fs.FileInfo) error {
	if !owned(info) {
		return ErrUnavailable
	}
	return os.Chmod(dir, 0o700)
}

func checkPrivate(_ string, info fs.FileInfo) error {
	if !owned(info) || info.Mode().Perm()&0o077 != 0 {
		return ErrUnavailable
	}
	return nil
}
