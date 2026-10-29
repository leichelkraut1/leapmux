//go:build !windows

package codex

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
