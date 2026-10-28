package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexFileAttachmentsReachSendAndSteerAsDurableReferences(t *testing.T) {
	for _, attachment := range []*leapmuxv1.Attachment{
		{Filename: "manual.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7\nexact bytes")},
		{Filename: "project.acd", MimeType: "application/octet-stream", Data: []byte{0, 255, 1, 2}},
	} {
		for _, steer := range []bool{false, true} {
			t.Run(attachment.Filename+"/"+strconv.FormatBool(steer), func(t *testing.T) {
				var a *Agent
				var requests func() []codexRecordedRequest
				a, _, requests = newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
					if method == "turn/start" {
						feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1"}}}`)
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				})
				a.threadID = "thread-1"
				a.attachmentDir = filepath.Join(t.TempDir(), "attachments", "chat")
				var err error
				if steer {
					a.mainTurn.id = "turn-1"
					err = a.SteerInput("Inspect the file.", []*leapmuxv1.Attachment{attachment})
				} else {
					err = a.SendInput("Inspect the file.", []*leapmuxv1.Attachment{attachment})
				}
				require.NoError(t, err)
				sent := requests()
				require.Len(t, sent, 1)
				if steer {
					assert.Equal(t, "turn/steer", sent[0].Method)
				} else {
					assert.Equal(t, "turn/start", sent[0].Method)
				}
				input, ok := sent[0].Params["input"].([]any)
				require.True(t, ok)
				require.Len(t, input, 2)
				block, ok := input[1].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "text", block["type"])
				assert.NotContains(t, block, "url")
				entries, err := os.ReadDir(a.attachmentDir)
				require.NoError(t, err)
				require.Len(t, entries, 1)
				canonical, err := filepath.EvalSymlinks(a.attachmentDir)
				require.NoError(t, err)
				path := filepath.Join(canonical, entries[0].Name())
				assert.Contains(t, block["text"], strconv.Quote(path))
				actual, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, attachment.Data, actual)
				// A new agent instance uses the same file after restore; no in-memory
				// temporary directory owns the bytes or deletes them with the process.
				restored, err := buildCodexInputBlocks("", agent.ClassifyAttachments([]*leapmuxv1.Attachment{attachment}), a.attachmentDir)
				require.NoError(t, err)
				require.Len(t, restored, 1)
				assert.Contains(t, restored[0]["text"], strconv.Quote(path))
				actual, err = os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, attachment.Data, actual)
			})
		}
	}
}

func TestCodexFailedFileStagingReleasesSteerAdmission(t *testing.T) {
	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.threadID, a.mainTurn.id = "thread-1", "turn-1"
	attachment := &leapmuxv1.Attachment{Filename: "manual.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}
	require.ErrorContains(t, a.SteerInput("Read it.", []*leapmuxv1.Attachment{attachment}), "not configured")
	assert.Empty(t, requests())
	assert.Zero(t, a.mainTurn.steers)
	require.NoError(t, a.SteerInput("Continue without the file.", nil))
	require.Len(t, requests(), 1)
}
