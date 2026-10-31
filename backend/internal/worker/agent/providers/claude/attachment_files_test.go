package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeBinaryAttachmentsUseDurableFilesOnSendAndSteer(t *testing.T) {
	for _, steer := range []bool{false, true} {
		t.Run(strconv.FormatBool(steer), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "attachments", "chat")
			a, stdin := newClaudeContentAgent(t, steer)
			a.attachmentDir = dir
			attachment := &leapmuxv1.Attachment{Filename: "../../device-project.acd", MimeType: "application/octet-stream", Data: []byte{0, 255, 1, 2}}
			var err error
			if steer {
				err = a.SteerInput("Inspect the project.", []*leapmuxv1.Attachment{attachment})
			} else {
				err = a.SendInput("Inspect the project.", []*leapmuxv1.Attachment{attachment})
			}
			require.NoError(t, err)
			var envelope struct {
				Priority string `json:"priority"`
				Message  struct {
					Content []map[string]any `json:"content"`
				} `json:"message"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdin.String()), &envelope))
			require.Len(t, envelope.Message.Content, 2)
			assert.Equal(t, "text", envelope.Message.Content[0]["type"])
			assert.NotContains(t, envelope.Message.Content[0], "source")
			assert.Equal(t, map[string]any{"type": "text", "text": "Inspect the project."}, envelope.Message.Content[1])
			if steer {
				assert.Equal(t, "next", envelope.Priority)
			}
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			canonical, err := filepath.EvalSymlinks(dir)
			require.NoError(t, err)
			path := filepath.Join(canonical, entries[0].Name())
			assert.Contains(t, envelope.Message.Content[0]["text"], strconv.Quote(path))
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, attachment.Data, actual)

			a.Stop()
			actual, err = os.ReadFile(path)
			require.NoError(t, err, "stopping a process must preserve attachment history")
			assert.Equal(t, attachment.Data, actual)
			restored, restoredInput := newClaudeContentAgent(t, false)
			restored.attachmentDir = dir
			require.NoError(t, restored.SendInput("Reopen the same attachment.", []*leapmuxv1.Attachment{attachment}))
			assert.Contains(t, restoredInput.String(), "File available on this Worker")
			entries, err = os.ReadDir(dir)
			require.NoError(t, err)
			assert.Len(t, entries, 1, "resume/retry reuses the immutable file")
		})
	}
}

func TestClaudeMissingAttachmentStorageDoesNotSendPartialInput(t *testing.T) {
	a, stdin := newClaudeContentAgent(t, false)
	attachments := []*leapmuxv1.Attachment{claudeOrderPNG(), {Filename: "file.bin", MimeType: "application/octet-stream", Data: []byte{255, 0}}}
	require.ErrorContains(t, a.SendInput("Inspect both.", attachments), "not configured")
	assert.Empty(t, stdin.String())
}
