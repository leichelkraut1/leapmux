//go:build unix

package codex

import (
	"encoding/base64"
	"testing"

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
