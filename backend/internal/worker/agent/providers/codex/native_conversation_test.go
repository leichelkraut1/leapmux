package codex

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexConversationFixtureID = "11111111-1111-4111-8111-111111111111"

func codexConversationFixture(t *testing.T, archived bool) (string, string, string, string, *sql.DB) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0o700))
	cwd := filepath.Join(root, "harmless-project")
	index := filepath.Join(root, "state_5.sqlite")
	path := filepath.Join(root, "sessions", "2026", "10", "28", "rollout-"+codexConversationFixtureID+".jsonl")
	if archived {
		path = filepath.Join(root, "archived_sessions", "rollout-"+codexConversationFixtureID+".jsonl")
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	db := agenttest.NewFixtureDB(t, index, codexThreadsDDL)
	_, err = db.Exec("INSERT INTO threads(id,rollout_path,cwd,name,archived) VALUES(?,?,?,?,?)", codexConversationFixtureID, path, cwd, "Named external chat", archived)
	require.NoError(t, err)
	metadata, err := json.Marshal(map[string]any{"timestamp": "2026-10-28T00:00:00Z", "type": "session_meta", "payload": map[string]any{"id": codexConversationFixtureID, "cwd": cwd}})
	require.NoError(t, err)
	text := "{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"Harmless question\"}]}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Harmless reply\"}]}}\n"
	require.NoError(t, os.WriteFile(path, append(append(metadata, '\n'), []byte(text)...), 0o600))
	return root, index, path, cwd, db
}

func TestReadCodexNativeConversationPreservesNativeBytesAndTailCursor(t *testing.T) {
	home, index, path, cwd, _ := codexConversationFixture(t, false)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	query := agent.NativeConversationQuery{SessionID: codexConversationFixtureID, Direction: contracts.NativeConversationDirectionLatest, Limit: 2}
	page, err := readCodexNativeConversationAt(context.Background(), query, home, index)
	require.NoError(t, err)
	assert.Equal(t, contracts.NativeConversationFormatCodexRolloutJSONL, page.Format)
	assert.Equal(t, "Named external chat", page.DisplayName)
	assert.Equal(t, cwd, page.WorkingDir)
	require.Len(t, page.Records, 2)
	assert.Contains(t, string(page.Records[1].NativeJSON), "Harmless reply")
	assert.True(t, page.HasOlder)
	assert.Empty(t, page.PartialReason)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	query.Direction, query.Cursor = contracts.NativeConversationDirectionAfter, page.NewerCursor
	empty, err := readCodexNativeConversationAt(context.Background(), query, home, index)
	require.NoError(t, err)
	assert.Empty(t, empty.Records)
	assert.NotEmpty(t, empty.NewerCursor)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString("{\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"Next recorded reply\"}}\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	query.Cursor = empty.NewerCursor
	next, err := readCodexNativeConversationAt(context.Background(), query, home, index)
	require.NoError(t, err)
	require.Len(t, next.Records, 1)
	assert.Equal(t, page.Revision, next.Revision)
	assert.Contains(t, string(next.Records[0].NativeJSON), "Next recorded reply")
}

func TestReadCodexNativeConversationAllowsArchivedSavedHistory(t *testing.T) {
	home, index, _, _, _ := codexConversationFixture(t, true)
	page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
	require.NoError(t, err)
	require.Len(t, page.Records, 3)
	assert.Empty(t, page.PartialReason)
	assert.NotNil(t, Registration().ReadNativeConversation)
}

func TestReadCodexNativeConversationRejectsPathAndMetadataConfusion(t *testing.T) {
	for _, kind := range []string{"outside-root", "foreign-session", "foreign-cwd"} {
		t.Run(kind, func(t *testing.T) {
			home, index, path, cwd, db := codexConversationFixture(t, false)
			if kind == "outside-root" {
				_, err := db.Exec("UPDATE threads SET rollout_path=?", filepath.Join(filepath.Dir(home), "outside.jsonl"))
				require.NoError(t, err)
			} else {
				id := codexConversationFixtureID
				if kind == "foreign-session" {
					id = "22222222-2222-4222-8222-222222222222"
				} else {
					cwd = filepath.Join(home, "different-project")
				}
				data, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
			}
			page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
			require.Error(t, err)
			assert.Empty(t, page.Records)
			assert.NotEmpty(t, page.PartialReason)
			assert.NotContains(t, page.PartialReason, home)
		})
	}
}

func TestReadCodexNativeConversationRejectsViewsAndUnknownSchema(t *testing.T) {
	for _, schema := range []string{"CREATE VIEW threads AS SELECT 'id' AS id", "CREATE TABLE threads(id TEXT,rollout_path BLOB,cwd TEXT,name TEXT,archived INTEGER)"} {
		t.Run(strings.Split(schema, " ")[1], func(t *testing.T) {
			home, index, _, _, db := codexConversationFixture(t, false)
			_, err := db.Exec("DROP TABLE threads")
			require.NoError(t, err)
			_, err = db.Exec(schema)
			require.NoError(t, err)
			page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
			require.Error(t, err)
			assert.Empty(t, page.Records)
			assert.Equal(t, "unsupported_native_index", page.PartialReason)
		})
	}
}

func TestReadCodexNativeConversationDoesNotAcceptPathAsSessionID(t *testing.T) {
	home, index, _, _, _ := codexConversationFixture(t, false)
	page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: "../state_5.sqlite"}, home, index)
	require.Error(t, err)
	assert.Empty(t, page.Records)
	assert.Equal(t, "invalid_session_identity", page.PartialReason)
}
