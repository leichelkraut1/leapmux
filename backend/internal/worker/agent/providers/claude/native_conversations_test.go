package claude

import (
	"context"
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

func nativeClaudeFixture(t *testing.T, root, cwd, sessionID string, records ...string) string {
	t.Helper()
	project := mangleClaudePath(cwd)
	if len(project) > claudeMangleMaxLength {
		project = project[:claudeMangleMaxLength] + "-generated"
	}
	path := filepath.Join(root, claudeProjectsDirName, project, sessionID+".jsonl")
	agenttest.WriteFixtureFile(t, path, strings.Join(records, "\n")+"\n")
	return path
}

func TestClaudeNativeConversationKeepsNativeRecords(t *testing.T) {
	t.Parallel()
	root, cwd := t.TempDir(), t.TempDir()
	const session = "session-generated"
	header := `{"type":"file-history-snapshot","snapshot":{"trackedFileBackups":{}}}`
	user := claudeUserRecord(cwd, session, "Inspect the generated file")
	assistant := `{"type":"assistant","uuid":"generated-assistant","parentUuid":"generated-user","sessionId":"` + session + `","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Check it"},{"type":"text","text":"Here is the result"},{"type":"tool_use","id":"generated-tool","name":"Read","input":{"file_path":"generated.txt"}}]}}`
	title := `{"type":"custom-title","sessionId":"` + session + `","customTitle":"Generated fixture"}`
	aiTitle := `{"type":"ai-title","sessionId":"` + session + `","aiTitle":"Later generated AI title"}`
	nativeClaudeFixture(t, root, cwd, session, header, user, assistant, title, aiTitle)

	page, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: session})
	require.NoError(t, err)
	assert.Equal(t, contracts.NativeConversationFormatClaudeJSONL, page.Format)
	assert.Equal(t, session, page.SessionID)
	assert.Equal(t, cwd, page.WorkingDir)
	assert.Equal(t, "Generated fixture", page.DisplayName)
	assert.Contains(t, page.PartialReason, "branch selection")
	require.Len(t, page.Records, 5)
	assert.Equal(t, int64(1), page.Records[0].Seq)
	assert.Equal(t, int64(len(header)+len(user)+3), page.Records[2].Seq)
	assert.Equal(t, assistant, string(page.Records[2].NativeJSON), "provider envelopes must not be summarized or replayed")
}

func TestClaudeNativeConversationRejectsConflictingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, string)
	}{
		{"session", func(r map[string]any, _ string) { r["sessionId"] = "different-session" }},
		{"relative-cwd", func(r map[string]any, _ string) { r["cwd"] = "relative/generated" }},
		{"sidechain", func(r map[string]any, _ string) { r["isSidechain"] = true }},
		{"malformed-identity", func(r map[string]any, _ string) { r["sessionId"] = 7 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, cwd := t.TempDir(), t.TempDir()
			first := claudeUserRecord(cwd, "generated", "safe generated words")
			var conflicting map[string]any
			require.NoError(t, json.Unmarshal([]byte(first), &conflicting))
			tc.change(conflicting, cwd)
			raw, err := json.Marshal(conflicting)
			require.NoError(t, err)
			nativeClaudeFixture(t, root, cwd, "generated", first, string(raw))
			_, err = readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: "generated", Limit: 1})
			require.Error(t, err, "a matching earlier header must not admit later conflicting records")
		})
	}
}

func TestClaudeNativeConversationAllowsCwdChangesInTheBoundSession(t *testing.T) {
	t.Parallel()
	root, original, changed := t.TempDir(), t.TempDir(), t.TempDir()
	const session = "generated-cwd-change"
	first := claudeUserRecord(original, session, "generated original directory")
	second := claudeUserRecord(changed, session, "generated changed directory")
	path := nativeClaudeFixture(t, root, original, session, first, second)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	latest, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: session, Limit: 1})
	require.NoError(t, err)
	require.Len(t, latest.Records, 1)
	assert.Equal(t, second, string(latest.Records[0].NativeJSON))
	assert.Equal(t, original, latest.WorkingDir, "the native project stays bound to its original identity-bearing cwd")
	require.True(t, latest.HasOlder)
	older, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{
		SessionID: session, Cursor: latest.OlderCursor, Direction: contracts.NativeConversationDirectionBefore, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, older.Records, 1)
	assert.Equal(t, first, string(older.Records[0].NativeJSON))
	assert.Equal(t, original, older.WorkingDir)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	third := claudeUserRecord(original, session, "generated return to original directory")
	_, err = file.WriteString(third + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	more, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{
		SessionID: session, Cursor: latest.NewerCursor, Direction: contracts.NativeConversationDirectionAfter,
	})
	require.NoError(t, err)
	require.Len(t, more.Records, 1)
	assert.Equal(t, third, string(more.Records[0].NativeJSON))
	assert.Equal(t, latest.Revision, more.Revision)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, append(append([]byte(nil), before...), []byte(third+"\n")...), after, "reader must leave the native file unchanged")
}

func TestClaudeNativeConversationRequiresAnIdentityBoundProject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{"foreign-initial-cwd", ""},
		{"unbound-metadata-cannot-authorize-foreign-cwd", `{"type":"system","cwd":%s}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, original, foreign := t.TempDir(), t.TempDir(), t.TempDir()
			const session = "generated-origin-binding"
			var records []string
			if tc.prefix != "" {
				encoded, err := json.Marshal(original)
				require.NoError(t, err)
				records = append(records, strings.ReplaceAll(tc.prefix, "%s", string(encoded)))
			}
			records = append(records, claudeUserRecord(foreign, session, "generated foreign original"))
			nativeClaudeFixture(t, root, original, session, records...)
			_, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: session})
			require.Error(t, err)
		})
	}
}

func TestClaudeNativeConversationRequiresCwdAndUniqueFile(t *testing.T) {
	t.Parallel()
	root, cwd := t.TempDir(), t.TempDir()
	nativeClaudeFixture(t, root, cwd, "missing-cwd", `{"type":"user","sessionId":"missing-cwd","message":{"content":"generated"}}`)
	_, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: "missing-cwd"})
	require.Error(t, err)

	// Long mangled names have CLI-generated suffixes. Two verified files with
	// the same UUID are still ambiguous, even when their bytes are identical.
	longCwd := filepath.Join(cwd, strings.Repeat("x", 220))
	prefix := mangleClaudePath(longCwd)[:claudeMangleMaxLength]
	for _, suffix := range []string{"-first", "-second"} {
		path := filepath.Join(root, claudeProjectsDirName, prefix+suffix, "duplicate.jsonl")
		agenttest.WriteFixtureFile(t, path, claudeUserRecord(longCwd, "duplicate", "generated duplicate")+"\n")
	}
	_, err = readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: "duplicate"})
	require.Error(t, err)
}

func TestClaudeNativeConversationExplicitLookupAndAppend(t *testing.T) {
	t.Parallel()
	root, cwd := t.TempDir(), t.TempDir()
	const session = "saved-session"
	first := claudeUserRecord(cwd, session, "generated first")
	path := nativeClaudeFixture(t, root, cwd, session, first)
	page, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: session, Limit: 1})
	require.NoError(t, err, "saved sessions do not need a live process")
	require.NotEmpty(t, page.NewerCursor)

	second := `{"type":"assistant","sessionId":"saved-session","message":{"content":[{"type":"text","text":"generated append"}]}}`
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString(second + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	more, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{
		SessionID: session, Cursor: page.NewerCursor, Direction: contracts.NativeConversationDirectionAfter,
	})
	require.NoError(t, err)
	require.Len(t, more.Records, 1)
	assert.False(t, more.ResetRequired)
	assert.Equal(t, page.Revision, more.Revision)
	assert.Equal(t, int64(len(first)+2), more.Records[0].Seq)
	assert.Equal(t, second, string(more.Records[0].NativeJSON))
}

func TestClaudeNativeConversationLongMangledCwd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), strings.Repeat("generated", 35))
	project := mangleClaudePath(cwd)[:claudeMangleMaxLength] + "-cli-hash"
	path := filepath.Join(root, claudeProjectsDirName, project, "long-generated-session.jsonl")
	agenttest.WriteFixtureFile(t, path, claudeUserRecord(cwd, "long-generated-session", "generated long cwd")+"\n")
	page, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: "long-generated-session"})
	require.NoError(t, err)
	assert.Equal(t, cwd, page.WorkingDir)
	require.Len(t, page.Records, 1)
}

func TestClaudeNativeConversationRejectsPathsAndDoesNotReadSubagents(t *testing.T) {
	t.Parallel()
	root, cwd := t.TempDir(), t.TempDir()
	for _, id := range []string{"", "../other", "nested/session", `C:\\other`, "session.jsonl"} {
		_, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: id})
		require.Error(t, err)
	}
	subagent := filepath.Join(root, claudeProjectsDirName, mangleClaudePath(cwd), "parent", "subagents", "child.jsonl")
	agenttest.WriteFixtureFile(t, subagent, claudeUserRecord(cwd, "child", "generated child")+"\n")
	_, err := readClaudeNativeConversationAt(context.Background(), root, agent.NativeConversationQuery{SessionID: "child"})
	assert.ErrorIs(t, err, os.ErrNotExist)
}
