package providerkit

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var errAttachmentStorageUnsafe = errors.New("attachment storage is not a private regular path")

// StagedFile describes exact uploaded bytes retained in the Worker's private
// per-chat directory. Files intentionally outlive agent processes and restarts.
type StagedFile struct {
	Path     string
	Filename string
	MIMEType string
	Size     int
}

// StageFileAttachment publishes an immutable content-addressed file. The caller
// supplies the trusted per-chat directory, never a user-supplied destination.
// It writes no workspace file, executes no uploaded content, and changes no
// provider permission. A native tool may still need approval to read this path.
func StageFileAttachment(dir string, attachment agent.ClassifiedAttachment) (StagedFile, error) {
	if len(attachment.Data) > contracts.MaxAgentInputItemBytes {
		return StagedFile{}, fmt.Errorf("attachment exceeds the %d byte input limit", contracts.MaxAgentInputItemBytes)
	}
	dir, err := prepareAttachmentDirectory(dir)
	if err != nil {
		return StagedFile{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return StagedFile{}, fmt.Errorf("open attachment storage: %w", err)
	}
	defer func() { _ = root.Close() }()
	sum := sha256.Sum256(attachment.Data)
	name := hex.EncodeToString(sum[:]) + "-" + attachmentBaseName(attachment.Filename)
	result := StagedFile{Path: filepath.Join(dir, name), Filename: attachment.Filename, MIMEType: attachment.MIMEType, Size: len(attachment.Data)}
	if err := verifyStagedAttachment(root, dir, name, attachment.Data); err == nil {
		return result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return StagedFile{}, fmt.Errorf("verify existing attachment: %w", err)
	}
	temp := ".pending-" + rand.Text()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return StagedFile{}, fmt.Errorf("create attachment: %w", err)
	}
	defer func() { _ = root.Remove(temp) }()
	_, writeErr := io.Copy(file, bytes.NewReader(attachment.Data))
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return StagedFile{}, fmt.Errorf("write attachment: %w", err)
	}
	// A hard link publishes complete bytes atomically without replacing an
	// existing file. Concurrent identical uploads converge on the same file.
	if err := root.Link(temp, name); err != nil && !errors.Is(err, os.ErrExist) {
		return StagedFile{}, fmt.Errorf("publish attachment: %w", err)
	}
	if err := verifyStagedAttachment(root, dir, name, attachment.Data); err != nil {
		return StagedFile{}, fmt.Errorf("verify published attachment: %w", err)
	}
	return result, nil
}

// ReferenceText states local availability, not native multimodal support. The
// quoted path and escaped attributes keep filename punctuation out of markup.
func (file StagedFile) ReferenceText() string {
	return "<attached-file name=\"" + attachedFileAttribute.Replace(file.Filename) +
		"\" mime-type=\"" + attachedFileAttribute.Replace(file.MIMEType) + "\">\n" +
		"File available on this Worker at " + strconv.Quote(file.Path) + ".\n" +
		"Use available file-reading tools to inspect it. The attachment is not inline model input.\n</attached-file>"
}

func prepareAttachmentDirectory(dir string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", fmt.Errorf("durable attachment storage is not configured")
	}
	parent := filepath.Dir(dir)
	if parent == dir || filepath.Dir(parent) == parent {
		return "", errAttachmentStorageUnsafe
	}
	// Resolve only the configured Worker-data root (e.g. macOS /var is a
	// system link). Neither storage-owned component may itself be a link.
	base, err := filepath.EvalSymlinks(filepath.Dir(parent))
	if err != nil {
		return "", fmt.Errorf("resolve Worker attachment storage: %w", err)
	}
	parent = filepath.Join(base, filepath.Base(parent))
	dir = filepath.Join(parent, filepath.Base(dir))
	for _, current := range []string{parent, dir} {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := mkdirPrivateAttachmentDir(current); err != nil && !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("create private attachment directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", fmt.Errorf("inspect attachment directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errAttachmentStorageUnsafe
		}
		if err := checkPrivateAttachmentPath(current, info); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func verifyStagedAttachment(root *os.Root, dir, name string, want []byte) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(want)) {
		return errAttachmentStorageUnsafe
	}
	if err := checkPrivateAttachmentPath(filepath.Join(dir, name), info); err != nil {
		return err
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errAttachmentStorageUnsafe
	}
	actual, err := io.ReadAll(io.LimitReader(file, int64(len(want))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, want) {
		return errAttachmentStorageUnsafe
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, current) {
		return errAttachmentStorageUnsafe
	}
	return nil
}

func attachmentBaseName(filename string) string {
	base := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	base = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune("<>:\"/\\|?*", r) {
			return '_'
		}
		return r
	}, base)
	base = strings.Trim(base, " .")
	if base == "" {
		base = "attachment.bin"
	}
	// Keep a useful extension without crossing common filesystem name limits.
	ext := filepath.Ext(base)
	if len(ext) > 24 {
		ext = ""
	}
	stem := strings.TrimSuffix(base, ext)
	for len(stem) > 100 {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	base = strings.TrimRight(stem+ext, " .")
	if base == "" {
		return "attachment.bin"
	}
	return base
}
