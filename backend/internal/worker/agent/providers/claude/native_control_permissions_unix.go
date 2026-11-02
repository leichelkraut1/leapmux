//go:build unix

package claude

import (
	"context"
	"os"
	"syscall"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/shirou/gopsutil/v4/process"
)

func nativeControlPrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return agent.ErrNativeControlUnavailable
	}
	return nativeControlCheckPrivate(path)
}

func nativeControlCheckPrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return agent.ErrNativeControlUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

func nativeControlLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := nativeControlCheckPrivate(path); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func nativeControlSameUser(ctx context.Context, pid int) bool {
	p, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return false
	}
	uids, err := p.UidsWithContext(ctx)
	return err == nil && len(uids) > 0 && uids[0] == uint32(os.Getuid())
}
