package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

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
			Name             string                        `json:"name"`
			Filename         string                        `json:"filename"`
			MIMEType         string                        `json:"mimeType"`
			DataBase64       string                        `json:"dataBase64"`
			ExpectedMIMEType string                        `json:"expectedMimeType"`
			ExpectedKind     contracts.AttachmentMediaKind `json:"expectedKind"`
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

func TestClassifyAttachmentsInfersNativeAudio(t *testing.T) {
	t.Parallel()
	for filename, mime := range map[string]string{"test.wav": "audio/wav", "test.MP3": "audio/mpeg", "test.m4a": "audio/mp4", "test.webm": "audio/webm", "test.ogg": "audio/ogg"} {
		for _, given := range []string{"", "application/octet-stream"} {
			data := []byte{0, 1, 2}
			attachments := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: given, Data: data}})
			require.Len(t, attachments, 1)
			assert.Equal(t, contracts.AttachmentMediaKindAudio, attachments[0].Kind)
			assert.Equal(t, mime, attachments[0].MIMEType)
			assert.Equal(t, data, attachments[0].Data)
		}
	}
}

func TestAudioHasAnIndependentAttachmentKind(t *testing.T) {
	t.Parallel()
	classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 1}}})
	require.Len(t, classified, 1)
	assert.Equal(t, contracts.AttachmentMediaKind("audio"), classified[0].Kind)
}

func TestClassifyAttachmentsInfersFLACAndAAC(t *testing.T) {
	t.Parallel()
	for filename, mime := range map[string]string{"tone.flac": "audio/flac", "tone.AAC": "audio/aac"} {
		for _, supplied := range []string{"", "application/octet-stream"} {
			for _, data := range [][]byte{nil, []byte("audio bytes"), {0xff, 0}} {
				classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: supplied, Data: data}})
				require.Len(t, classified, 1)
				assert.Equal(t, contracts.AttachmentMediaKindAudio, classified[0].Kind, filename)
				assert.Equal(t, mime, classified[0].MIMEType, filename)
				assert.Equal(t, data, classified[0].Data)
			}
		}
		classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: "text/plain", Data: []byte("explicit text")}})
		require.Len(t, classified, 1)
		assert.Equal(t, contracts.AttachmentMediaKindText, classified[0].Kind)
	}
}

func TestClassifyAttachmentsInfersOfficeDocuments(t *testing.T) {
	t.Parallel()
	for filename, mime := range map[string]string{
		"native.doc":  "application/msword",
		"native.DOCX": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"native.xls":  "application/vnd.ms-excel",
		"native.XLSX": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	} {
		for _, given := range []string{"", "application/octet-stream"} {
			for _, data := range [][]byte{nil, []byte("Office bytes"), {0xff, 0x00}} {
				classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: filename, MimeType: given, Data: data}})
				require.Len(t, classified, 1)
				assert.Equal(t, contracts.AttachmentMediaKindBinary, classified[0].Kind, filename)
				assert.Equal(t, mime, classified[0].MIMEType, filename)
				assert.Equal(t, data, classified[0].Data)
			}
		}
	}
	classified := ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: "native.docx", MimeType: "text/plain", Data: []byte("explicit text")}})
	require.Len(t, classified, 1)
	assert.Equal(t, contracts.AttachmentMediaKindText, classified[0].Kind, "an explicit text MIME remains authoritative")
}
