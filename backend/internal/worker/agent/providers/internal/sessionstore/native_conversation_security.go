package sessionstore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrNativeConversationUnsafe = errors.New("native conversation source is not a verified same-user file")
	ErrNativeConversationLimit  = errors.New("native conversation source exceeds the bounded discovery limit")
)

// nativeRelative admits only provider-derived paths below one configured root.
// Browser cursors never carry a filesystem path.
func nativeRelative(value string) (string, error) {
	value = filepath.ToSlash(value)
	if value == "" || strings.ContainsAny(value, "\\\x00") || !filepath.IsLocal(value) ||
		filepath.ToSlash(filepath.Clean(value)) != value || value == "." {
		return "", ErrNativeConversationUnsafe
	}
	return filepath.FromSlash(value), nil
}

func nativeNoLinks(value string) error {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || !nativeRootAllowed(value) {
		return ErrNativeConversationUnsafe
	}
	for current := value; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || nativeReparsePoint(current) {
			return ErrNativeConversationUnsafe
		}
		if parent := filepath.Dir(current); parent == current {
			return nil
		}
	}
}

func nativeOwnedPath(root, relative string, directory bool) (os.FileInfo, error) {
	if err := nativeNoLinks(root); err != nil {
		return nil, err
	}
	info, err := nativeOwned(root, true)
	if err != nil {
		return nil, err
	}
	if relative == "" {
		return info, nil
	}
	relative, err = nativeRelative(relative)
	if err != nil {
		return nil, err
	}
	current := root
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		if err := nativeNoLinks(current); err != nil {
			return nil, err
		}
		info, err = nativeOwned(current, index < len(parts)-1 || directory)
		if err != nil {
			return nil, err
		}
	}
	return info, nil
}

// VerifyNativeConversationFilePath checks a provider's own index file with the
// same owner/link rules as transcript files, before a read-only database open.
func VerifyNativeConversationFilePath(root, relative string) error {
	_, err := nativeOwnedPath(root, relative, false)
	return err
}

// ReadNativeConversationDirectories returns an exhaustive bounded list of
// verified child directory names. Overflow is an error, never a partial list
// that could falsely prove a session filename unique.
func ReadNativeConversationDirectories(root, relative string, limit int) ([]string, error) {
	if limit <= 0 || limit > 10000 {
		return nil, ErrNativeConversationLimit
	}
	checked, err := nativeOwnedPath(root, relative, true)
	if err != nil {
		return nil, err
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()
	openRelative := relative
	if openRelative == "" {
		openRelative = "."
	}
	directory, err := handle.Open(openRelative)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(checked, opened) {
		return nil, ErrNativeConversationUnsafe
	}
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, ErrNativeConversationUnsafe
	}
	if len(entries) > limit {
		return nil, ErrNativeConversationLimit
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, ErrNativeConversationUnsafe
		}
		if !entry.IsDir() {
			continue
		}
		if _, err := nativeOwnedPath(root, filepath.Join(relative, entry.Name()), true); err != nil {
			return nil, err
		}
		result = append(result, entry.Name())
	}
	after, err := nativeOwnedPath(root, relative, true)
	if err != nil || !os.SameFile(opened, after) {
		return nil, ErrNativeConversationUnsafe
	}
	return result, nil
}
