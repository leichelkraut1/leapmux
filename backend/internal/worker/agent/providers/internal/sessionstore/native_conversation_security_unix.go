//go:build !windows

package sessionstore

import (
	"fmt"
	"os"
	"syscall"
)

func nativeReparsePoint(string) bool { return false }
func nativeRootAllowed(string) bool  { return true }

func nativeOpenedFileOwned(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return ErrNativeConversationUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 || !info.Mode().IsRegular() {
		return ErrNativeConversationUnsafe
	}
	return nil
}

func nativeOwned(value string, directory bool) (os.FileInfo, error) {
	info, err := os.Lstat(value)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 ||
		(directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return nil, ErrNativeConversationUnsafe
	}
	return info, nil
}

func nativeFileIdentity(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ErrNativeConversationUnsafe
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
