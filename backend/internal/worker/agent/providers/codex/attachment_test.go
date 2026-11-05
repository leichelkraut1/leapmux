//go:build unix

package codex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCodexInputBlocks_imageAttachment(t *testing.T) {
	t.Parallel()

	data := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "photo.jpg", MimeType: "image/jpeg", Data: data},
	}
	blocks, err := buildCodexInputBlocks("describe this", agent.ClassifyAttachments(attachments), "")
	require.NoError(t, err)
	require.Len(t, blocks, 2)

	textBlock := blocks[0]
	assert.Equal(t, "text", textBlock["type"])

	imgBlock := blocks[1]
	assert.Equal(t, "image", imgBlock["type"])
	expectedURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
	assert.Equal(t, expectedURI, imgBlock["url"])
}

func TestBuildCodexInputBlocks_textAttachment(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "report.csv", MimeType: "", Data: []byte("name,value\nfoo,1\n")},
	}
	blocks, err := buildCodexInputBlocks("", agent.ClassifyAttachments(attachments), "")
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	assert.Equal(t, "text", blocks[0]["type"])
	assert.Contains(t, blocks[0]["text"], `<attached-file name="report.csv" mime-type="text/csv">`)
	assert.Contains(t, blocks[0]["text"], "name,value")
}

func TestBuildCodexInputBlocks_PDFRequiresDurableStorage(t *testing.T) {
	attachments := []*leapmuxv1.Attachment{{Filename: "doc.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}}
	blocks, err := buildCodexInputBlocks("", agent.ClassifyAttachments(attachments), "")
	require.ErrorContains(t, err, "not configured")
	require.Nil(t, blocks)
}

func TestNormalizeAttachmentsForProvider_CodexAcceptsPDFAndBinary(t *testing.T) {
	for _, attachment := range []*leapmuxv1.Attachment{
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
		{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0}},
	} {
		normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, []*leapmuxv1.Attachment{attachment})
		require.NoError(t, err)
		require.Len(t, normalized, 1)
		assert.Equal(t, attachment.Data, normalized[0].Data)
	}
}

func TestBuildCodexInputBlocksAudio(t *testing.T) {
	t.Parallel()
	data := []byte{0, 1, 2, 3}
	attachments := []*leapmuxv1.Attachment{{Filename: "sample.wav", MimeType: "audio/wav", Data: data}}
	blocks, err := buildCodexInputBlocks("Listen.", agent.ClassifyAttachments(attachments), "")
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	assert.Equal(t, "text", blocks[0]["type"])
	assert.Equal(t, "audio", blocks[1]["type"])
	assert.Equal(t, "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(data), blocks[1]["url"])
	_, err = agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, attachments)
	require.NoError(t, err)
}

func TestCodexAudioValidationAcceptsFilesAndRejectsEmptyAudio(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	for _, mime := range []string{"audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave", "audio/mpeg", "audio/mp3", "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/webm", "audio/ogg", "audio/flac"} {
		attachment := agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindAudio, MIMEType: mime, Filename: "audio", Data: []byte{0, 1}}
		require.NoError(t, provider.ValidateAttachment(attachment))
		attachment.Data = nil
		require.ErrorContains(t, provider.ValidateAttachment(attachment), "empty")
	}
	for _, mime := range []string{"audio/flac", "application/zip", "application/octet-stream", ""} {
		require.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindBinary, MIMEType: mime, Data: []byte{0, 1}}))
	}
}

func TestCodexAudioModelValidation(t *testing.T) {
	t.Parallel()
	a := &Agent{availableModels: []*agent.ModelInfo{nil, {Id: "audio", SupportsAudioInput: true}, {Id: "text"}}}
	input := []map[string]interface{}{{"type": "audio", "url": "data:audio/wav;base64,AAE="}}
	require.NoError(t, a.validateCodexAudioInputLocked(input, "audio"))
	for _, model := range []string{"text", "", "unknown"} {
		require.ErrorContains(t, a.validateCodexAudioInputLocked(input, model), "does not accept audio")
	}
	require.NoError(t, a.validateCodexAudioInputLocked(nil, "unknown"))
	require.NoError(t, a.validateCodexAudioInputLocked([]map[string]interface{}{{"type": "text", "text": "Hello"}}, "text"))
}

func TestCodexAudioDispatchAndSteerKeepTheRunningModel(t *testing.T) {
	t.Parallel()
	var subject *Agent
	a, _, requests := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		if method == "turn/start" {
			subject.handleMainTurnStarted("audio-turn")
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	subject = a
	a.threadID = "audio-thread"
	a.model = "audio-model"
	a.availableModels = []*agent.ModelInfo{{Id: "audio-model", SupportsAudioInput: true}, {Id: "text-model"}}
	attachments := []*leapmuxv1.Attachment{{Filename: "sample.wav", MimeType: "audio/wav", Data: []byte{0, 1}}}
	require.NoError(t, a.SendInput("Listen.", attachments))
	assert.Equal(t, "audio-model", a.mainTurn.model)
	a.model = "text-model"
	require.NoError(t, a.SteerInput("Listen again.", attachments))
	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, "turn/start", recorded[0].Method)
	assert.Equal(t, "turn/steer", recorded[1].Method)
	for _, request := range recorded {
		input, ok := request.Params["input"].([]interface{})
		require.True(t, ok)
		require.Len(t, input, 2)
		assert.Equal(t, map[string]interface{}{"type": "audio", "url": "data:audio/wav;base64,AAE="}, input[1])
	}
}

func TestCodexAudioFallbackWithoutStorageDoesNotDispatch(t *testing.T) {
	t.Parallel()
	for _, current := range []string{"text-model", "unknown", ""} {
		a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.threadID = "audio-thread"
		a.model = current
		a.availableModels = []*agent.ModelInfo{{Id: "audio-model", SupportsAudioInput: true}, {Id: "text-model"}}
		attachments := []*leapmuxv1.Attachment{{Filename: "sample.wav", MimeType: "audio/wav", Data: []byte{0, 1}}}
		require.ErrorContains(t, a.SendInput("Listen.", attachments), "not configured")
		assert.Nil(t, a.pendingStart)
		a.mainTurn = codexMainTurn{id: "text-turn", model: current}
		a.model = "audio-model"
		require.ErrorContains(t, a.SteerInput("Listen.", attachments), "not configured")
		assert.Zero(t, a.mainTurn.steers)
		assert.Empty(t, a.pendingSteerCalls)
		assert.Empty(t, requests())
	}
}

func TestCodexAudioFallsBackToOneDurableFileDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ model, mime string }{
		{"text-model", "audio/wav"},
		{"unknown", "audio/wav"},
		{"", "audio/wav"},
		{"audio-model", "audio/flac"},
	} {
		for _, steer := range []bool{false, true} {
			t.Run(tc.model+"/"+tc.mime+"/"+strconv.FormatBool(steer), func(t *testing.T) {
				var subject *Agent
				a, _, requests := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
					if method == "turn/start" {
						subject.handleMainTurnStarted("fallback-turn")
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				})
				subject = a
				a.threadID, a.model = "file-thread", tc.model
				a.attachmentDir = filepath.Join(t.TempDir(), "attachments", "chat")
				a.availableModels = []*agent.ModelInfo{{Id: "audio-model", SupportsAudioInput: true}, {Id: "text-model"}}
				data := []byte{0, 255, 1, 2}
				attachments := []*leapmuxv1.Attachment{{Filename: "sample.audio", MimeType: tc.mime, Data: data}}
				if steer {
					a.mainTurn = codexMainTurn{id: "running-turn", model: tc.model}
					a.model = "audio-model" // Steering keeps the running turn's model.
					require.NoError(t, a.SteerInput("Inspect the recording.", attachments))
				} else {
					require.NoError(t, a.SendInput("Inspect the recording.", attachments))
				}
				recorded := requests()
				require.Len(t, recorded, 1, "an incapable model receives only the file-backed input")
				input, ok := recorded[0].Params["input"].([]interface{})
				require.True(t, ok)
				require.Len(t, input, 2)
				block, ok := input[1].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "text", block["type"])
				assert.NotContains(t, block, "url")
				entries, err := os.ReadDir(a.attachmentDir)
				require.NoError(t, err)
				require.Len(t, entries, 1)
				canonical, err := filepath.EvalSymlinks(a.attachmentDir)
				require.NoError(t, err)
				file := filepath.Join(canonical, entries[0].Name())
				actual, err := os.ReadFile(file)
				require.NoError(t, err)
				assert.Equal(t, data, actual)
				assert.Contains(t, block["text"], strconv.Quote(file))
			})
		}
	}
}

func TestCodexNativeAudioFailureDoesNotReplayAsFile(t *testing.T) {
	t.Parallel()
	for _, steer := range []bool{false, true} {
		t.Run(strconv.FormatBool(steer), func(t *testing.T) {
			a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				// Matching the local guard's words does not make a native error
				// evidence that no bytes were delivered.
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"native audio is unavailable for this model"}`)}
			})
			a.threadID, a.model = "audio-thread", "audio-model"
			a.availableModels = []*agent.ModelInfo{{Id: "audio-model", SupportsAudioInput: true}}
			a.attachmentDir = filepath.Join(t.TempDir(), "attachments", "chat")
			attachments := []*leapmuxv1.Attachment{{Filename: "sample.wav", MimeType: "audio/wav", Data: []byte{0, 1}}}
			var err error
			if steer {
				a.mainTurn = codexMainTurn{id: "audio-turn", model: "audio-model"}
				err = a.SteerInput("Listen.", attachments)
			} else {
				err = a.SendInput("Listen.", attachments)
			}
			require.Error(t, err)
			recorded := requests()
			require.Len(t, recorded, 1, "a native failure cannot replay the input through file fallback")
			input, ok := recorded[0].Params["input"].([]interface{})
			require.True(t, ok)
			require.Len(t, input, 2)
			block, ok := input[1].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, "audio", block["type"])
			_, err = os.Stat(a.attachmentDir)
			assert.True(t, os.IsNotExist(err), "no fallback file is staged after a native failure")
		})
	}
}
