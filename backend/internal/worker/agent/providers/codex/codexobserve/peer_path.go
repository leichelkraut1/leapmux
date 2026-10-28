//go:build windows || darwin

package codexobserve

import (
	"os"
	"path/filepath"
	"strings"
)

// noLinkedComponents rejects aliases before owner checks or file reads. It
// follows no symlink and creates nothing.
func noLinkedComponents(value string) error {
	if !filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
		return errPeer
	}
	for current := filepath.Clean(value); ; {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errPeer
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}
