package codexobserve

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Create only missing components beneath an existing same-user, non-writable
// ancestor. Never chmod an existing directory or follow an alias. Each newly
// created component gets owner-only permissions before it can contain a file.
func controlPrivateDirectory(value string) error {
	if !filepath.IsAbs(value) {
		return agent.ErrNativeControlUnavailable
	}
	var missing []string
	current := value
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() || controlBaseDirectory(current) != nil {
				return agent.ErrNativeControlUnavailable
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return agent.ErrNativeControlUnavailable
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return agent.ErrNativeControlUnavailable
		}
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := controlMakePrivateDirectory(missing[index]); err != nil {
			return err
		}
	}
	return controlCheckPrivate(value)
}

func controlPrivatePath(value string) error {
	info, err := os.Lstat(value)
	if err != nil || !info.Mode().IsRegular() || controlCheckPrivate(value) != nil || controlCheckPrivate(filepath.Dir(value)) != nil {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}
