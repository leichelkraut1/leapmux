package providerkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAttachmentTestDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return filepath.Join(base, "attachments", "chat")
}

func TestStageFileAttachmentRetainsExactBytesAcrossRepeatedInstances(t *testing.T) {
	dir := newAttachmentTestDir(t)
	attachment := agent.ClassifiedAttachment{Filename: "report.xlsx", MIMEType: "application/octet-stream", Data: []byte{0, 255, 1, 0, 128}, Kind: agent.AttachmentKindBinary}
	first, err := StageFileAttachment(dir, attachment)
	require.NoError(t, err)
	data, err := os.ReadFile(first.Path)
	require.NoError(t, err)
	assert.Equal(t, attachment.Data, data)
	sum := sha256.Sum256(attachment.Data)
	assert.Equal(t, hex.EncodeToString(sum[:])+"-report.xlsx", filepath.Base(first.Path))
	// A later process has no in-memory store state to restore. It obtains the
	// same immutable path, including when the first delivery needed a retry.
	second, err := StageFileAttachment(dir, attachment)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	attachment.Data = []byte{0, 255, 2}
	third, err := StageFileAttachment(dir, attachment)
	require.NoError(t, err)
	assert.NotEqual(t, first.Path, third.Path)
	data, err = os.ReadFile(first.Path)
	require.NoError(t, err)
	assert.Equal(t, []byte{0, 255, 1, 0, 128}, data, "a duplicate filename cannot overwrite earlier history")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "successful writes leave no temporary files")
}

func TestStageFileAttachmentContainsUntrustedNamesAndEscapesReferences(t *testing.T) {
	dir := newAttachmentTestDir(t)
	for _, name := range []string{"../../outside.bin", "C:\\other\\CON:stream.exe", "\"\n</attached-file>.bin", strings.Repeat("x", 99) + " " + strings.Repeat("y", 20), strings.Repeat("z", 99) + "." + strings.Repeat("x", 30), strings.Repeat("界", 200) + ".bin"} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			file, err := StageFileAttachment(dir, agent.ClassifiedAttachment{Filename: name, MIMEType: "application/octet-stream", Data: []byte{255, 0}})
			require.NoError(t, err)
			relative, err := filepath.Rel(dir, file.Path)
			require.NoError(t, err)
			assert.Equal(t, filepath.Base(file.Path), relative)
			assert.LessOrEqual(t, len(relative), 190)
			assert.NotContains(t, relative, ":")
			assert.False(t, strings.HasSuffix(relative, " ") || strings.HasSuffix(relative, "."))
			reference := file.ReferenceText()
			assert.Contains(t, reference, strconv.Quote(file.Path))
			assert.NotContains(t, reference, "\n</attached-file>.bin")
			assert.Contains(t, reference, "not inline model input")
		})
	}
}

func TestStageFileAttachmentDoesNotOverwriteCorruption(t *testing.T) {
	dir := newAttachmentTestDir(t)
	attachment := agent.ClassifiedAttachment{Filename: "archive.zip", Data: []byte{255, 0, 1}}
	file, err := StageFileAttachment(dir, attachment)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file.Path, []byte{255, 0, 2}, 0o600))
	_, err = StageFileAttachment(dir, attachment)
	require.ErrorIs(t, err, errAttachmentStorageUnsafe)
	actual, err := os.ReadFile(file.Path)
	require.NoError(t, err)
	assert.Equal(t, []byte{255, 0, 2}, actual, "a changed existing path is rejected, never replaced")
}

func TestStageFileAttachmentRejectsLinkedStorageAndLeavesTargetsUnchanged(t *testing.T) {
	for _, component := range []string{"parent", "chat", "file"} {
		t.Run(component, func(t *testing.T) {
			dir := newAttachmentTestDir(t)
			target := t.TempDir()
			attachment := agent.ClassifiedAttachment{Filename: "archive.bin", Data: []byte{255, 0}}
			var link, destination string
			if component == "parent" {
				link, destination = filepath.Dir(dir), target
			} else {
				prepared, err := prepareAttachmentDirectory(dir)
				require.NoError(t, err)
				dir = prepared
				if component == "chat" {
					require.NoError(t, os.Remove(dir))
					link, destination = dir, target
				} else {
					sum := sha256.Sum256(attachment.Data)
					link = filepath.Join(dir, hex.EncodeToString(sum[:])+"-archive.bin")
					destination = filepath.Join(target, "keep.bin")
					require.NoError(t, os.WriteFile(destination, attachment.Data, 0o600))
				}
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Skipf("symlink creation is unavailable: %v", err)
			}
			_, err := StageFileAttachment(dir, attachment)
			require.Error(t, err)
			entries, err := os.ReadDir(target)
			require.NoError(t, err)
			if component == "file" {
				assert.Len(t, entries, 1)
			} else {
				assert.Empty(t, entries)
			}
		})
	}
}

func TestStageFileAttachmentConcurrentRetriesConverge(t *testing.T) {
	dir := newAttachmentTestDir(t)
	attachment := agent.ClassifiedAttachment{Filename: "same.bin", Data: bytes.Repeat([]byte{0, 255}, 1024)}
	var wg sync.WaitGroup
	results := make(chan StagedFile, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			file, err := StageFileAttachment(dir, attachment)
			results <- file
			failures <- err
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var path string
	for file := range results {
		if path == "" {
			path = file.Path
		}
		assert.Equal(t, path, file.Path)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestStageFileAttachmentRequiresStorageAndEnforcesSize(t *testing.T) {
	_, err := StageFileAttachment("", agent.ClassifiedAttachment{Data: []byte{255}})
	require.ErrorContains(t, err, "not configured")
	_, err = StageFileAttachment("relative", agent.ClassifiedAttachment{Data: []byte{255}})
	require.ErrorContains(t, err, "not configured")
	dir := newAttachmentTestDir(t)
	_, err = StageFileAttachment(dir, agent.ClassifiedAttachment{Data: make([]byte, contracts.MaxAgentInputItemBytes+1)})
	require.ErrorContains(t, err, "input limit")
	assert.NoDirExists(t, dir)
}
