package claude

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

//go:embed all:native-control-plugin
var nativeControlPlugin embed.FS

func (c *nativeControlController) writePlugin(descriptorPath string) error {
	descriptorJSON, err := json.Marshal(filepath.ToSlash(descriptorPath))
	if err != nil {
		return err
	}
	err = fs.WalkDir(nativeControlPlugin, "native-control-plugin", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative := strings.TrimPrefix(path, "native-control-plugin")
		relative = strings.TrimPrefix(relative, "/")
		destination := filepath.Join(c.directory, "plugin", filepath.FromSlash(relative))
		if entry.IsDir() {
			return nativeControlPrivateDirectory(destination)
		}
		data, err := nativeControlPlugin.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), "__LEAPMUX_DESCRIPTOR_JSON__", string(descriptorJSON))
		text = strings.ReplaceAll(text, "__LEAPMUX_POLL_MS__", strconv.Itoa(contracts.NativeSessionControlPollMilliseconds))
		text = strings.ReplaceAll(text, "__LEAPMUX_RETENTION_MS__", strconv.Itoa(2*contracts.NativeSessionControlLeaseSeconds*1000))
		if info, err := os.Lstat(destination); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
			return fs.ErrPermission
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := atomicfile.WriteFile(destination, []byte(text), 0o600); err != nil {
			return err
		}
		return nativeControlCheckPrivate(destination)
	})
	if err != nil {
		return err
	}
	marketplaceDir := filepath.Join(c.directory, ".claude-plugin")
	if err := nativeControlPrivateDirectory(marketplaceDir); err != nil {
		return err
	}
	marketplace := []byte(`{"name":"leapmux-local-native-control","owner":{"name":"LeapMux"},"plugins":[{"name":"leapmux-native-control","source":"./plugin"}]}`)
	path := filepath.Join(marketplaceDir, "marketplace.json")
	if err := atomicfile.WriteFile(path, marketplace, 0o600); err != nil {
		return err
	}
	return nativeControlCheckPrivate(path)
}
