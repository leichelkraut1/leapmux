package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both languages replay the same corpus, including invalid UTF-8 and NUL bytes
// that a JSON text string cannot safely represent.
func TestClassifyAttachmentsConformance(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "testdata", "attachment_classification_conformance.json")
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { assert.NoError(t, file.Close()) }()
	var fixture struct {
		Readme string `json:"_readme"`
		Cases  []struct {
			Name             string         `json:"name"`
			Filename         string         `json:"filename"`
			MIMEType         string         `json:"mimeType"`
			DataBase64       string         `json:"dataBase64"`
			ExpectedMIMEType string         `json:"expectedMimeType"`
			ExpectedKind     AttachmentKind `json:"expectedKind"`
		} `json:"cases"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&fixture))
	require.NotEmpty(t, fixture.Cases)
	names := map[string]bool{}
	for _, tc := range fixture.Cases {
		require.False(t, names[tc.Name], "duplicate conformance name: %s", tc.Name)
		names[tc.Name] = true
		t.Run(tc.Name, func(t *testing.T) {
			data, err := base64.StdEncoding.Strict().DecodeString(tc.DataBase64)
			require.NoError(t, err)
			files := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: tc.Filename, MimeType: tc.MIMEType, Data: data}})
			require.Len(t, files, 1)
			assert.Equal(t, tc.ExpectedKind, files[0].Kind)
			assert.Equal(t, tc.ExpectedMIMEType, files[0].MIMEType)
			assert.Equal(t, tc.Filename, files[0].Filename)
			assert.Equal(t, data, files[0].Data, "classification does not transform the file")
		})
	}
}
