//go:build unix

package codexobserve

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func controlBaseDirectory(value string) error {
	if noLinkedComponents(value) != nil {
		return agent.ErrNativeControlUnavailable
	}
	info, err := os.Lstat(value)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return agent.ErrNativeControlUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

func controlMakePrivateDirectory(value string) error {
	if err := os.Mkdir(value, 0o700); err != nil && !os.IsExist(err) {
		return agent.ErrNativeControlUnavailable
	}
	if err := controlCheckPrivate(value); err != nil {
		return err
	}
	return controlSyncDirectory(filepath.Dir(value))
}

func controlCheckPrivate(value string) error {
	if noLinkedComponents(value) != nil {
		return agent.ErrNativeControlUnavailable
	}
	info, err := os.Lstat(value)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return agent.ErrNativeControlUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

func controlSyncDirectory(value string) error {
	f, err := os.Open(value)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
