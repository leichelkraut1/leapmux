//go:build !windows

package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCodexNativeConversationUnixRejectsCWDCaseDifference(t *testing.T) {
	home, index, path, cwd, _ := codexConversationFixture(t, false)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	end := bytes.IndexByte(before, '\n')
	require.NotEqual(t, -1, end)
	nativeCWD := filepath.Join(filepath.Dir(cwd), strings.ToUpper(filepath.Base(cwd)))
	require.NotEqual(t, cwd, nativeCWD)
	metadata, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": codexConversationFixtureID, "cwd": nativeCWD}})
	require.NoError(t, err)
	data := append(append(metadata, '\n'), before[end+1:]...)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
	require.Error(t, err)
	assert.Empty(t, page.Records)
	assert.Equal(t, "native_transcript_unavailable", page.PartialReason)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, after)
}

func TestReadCodexNativeConversationUnixDoesNotInterpretWindowsNamespaces(t *testing.T) {
	for _, value := range []string{`\\?\C:\history.jsonl`, `\\server\share\history.jsonl`, `C:\history.jsonl`} {
		t.Run(value, func(t *testing.T) {
			home, index, _, _, db := codexConversationFixture(t, false)
			_, err := db.Exec("UPDATE threads SET rollout_path=? WHERE id=?", value, codexConversationFixtureID)
			require.NoError(t, err)
			page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
			require.Error(t, err)
			assert.Empty(t, page.Records)
			assert.Equal(t, "native_session_path_unverified", page.PartialReason)
		})
	}
}
