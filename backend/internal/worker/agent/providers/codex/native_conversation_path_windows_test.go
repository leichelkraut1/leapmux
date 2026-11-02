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

func codexFixtureMetadataCWD(t *testing.T, path, cwd string) {
	t.Helper()
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	end := bytes.IndexByte(before, '\n')
	require.NotEqual(t, -1, end)
	metadata, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": codexConversationFixtureID, "cwd": cwd}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(append(metadata, '\n'), before[end+1:]...), 0o600))
}

func TestReadCodexNativeConversationWindowsVerbatimLocalDrivePaths(t *testing.T) {
	for _, shape := range []string{"observed-index-only", "metadata-only", "all-verbatim"} {
		t.Run(shape, func(t *testing.T) {
			home, index, path, cwd, db := codexConversationFixture(t, false)
			indexedPath, indexedCWD, nativeCWD := path, cwd, cwd
			if shape != "metadata-only" {
				indexedPath, indexedCWD = `\\?\`+path, `\\?\`+cwd
			}
			if shape != "observed-index-only" {
				nativeCWD = `\\?\` + cwd
			}
			_, err := db.Exec("UPDATE threads SET rollout_path=?,cwd=? WHERE id=?", indexedPath, indexedCWD, codexConversationFixtureID)
			require.NoError(t, err)
			codexFixtureMetadataCWD(t, path, nativeCWD)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
			require.NoError(t, err)
			require.Len(t, page.Records, 3)
			assert.Equal(t, cwd, page.WorkingDir)
			assert.Empty(t, page.PartialReason)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Equal(t, before[:bytes.IndexByte(before, '\n')], page.Records[0].NativeJSON)
		})
	}
}

func TestReadCodexNativeConversationWindowsVerbatimLongPath(t *testing.T) {
	home, index, path, cwd, db := codexConversationFixture(t, false)
	longPath := filepath.Join(home, "sessions", strings.Repeat("harmless-segment-", 7), strings.Repeat("another-segment-", 7), "rollout-"+codexConversationFixtureID+".jsonl")
	require.Greater(t, len(longPath), 260)
	require.NoError(t, os.MkdirAll(filepath.Dir(longPath), 0o700))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(longPath, data, 0o600))
	_, err = db.Exec("UPDATE threads SET rollout_path=?,cwd=? WHERE id=?", `\\?\`+longPath, `\\?\`+cwd, codexConversationFixtureID)
	require.NoError(t, err)
	page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
	require.NoError(t, err)
	require.Len(t, page.Records, 3)
	assert.Equal(t, cwd, page.WorkingDir)
}

func TestReadCodexNativeConversationWindowsCWDCaseMatchesWithoutChangingNativeBytes(t *testing.T) {
	for _, shape := range []string{"mixed-drive-and-components", "verbatim-index", "verbatim-metadata", "different-directory"} {
		t.Run(shape, func(t *testing.T) {
			home, index, path, cwd, db := codexConversationFixture(t, false)
			indexedCWD, nativeCWD := strings.ToUpper(cwd), strings.ToLower(cwd)
			if shape == "verbatim-index" {
				indexedCWD = `\\?\` + indexedCWD
			} else if shape == "verbatim-metadata" {
				nativeCWD = `\\?\` + nativeCWD
			} else if shape == "different-directory" {
				nativeCWD += "-foreign"
			}
			_, err := db.Exec("UPDATE threads SET cwd=? WHERE id=?", indexedCWD, codexConversationFixtureID)
			require.NoError(t, err)
			codexFixtureMetadataCWD(t, path, nativeCWD)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
			if shape == "different-directory" {
				require.Error(t, err)
				assert.Empty(t, page.Records)
			} else {
				require.NoError(t, err)
				require.Len(t, page.Records, 3)
				assert.Empty(t, page.PartialReason)
				assert.Equal(t, before[:bytes.IndexByte(before, '\n')], page.Records[0].NativeJSON)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestReadCodexNativeConversationWindowsRejectsForeignVerbatimScopeAndIdentity(t *testing.T) {
	for _, kind := range []string{"outside-root", "foreign-volume", "foreign-cwd", "native-device-cwd"} {
		t.Run(kind, func(t *testing.T) {
			home, index, path, cwd, db := codexConversationFixture(t, false)
			indexedPath := `\\?\` + path
			if kind == "outside-root" {
				indexedPath = `\\?\` + filepath.Join(filepath.Dir(home), "outside.jsonl")
			} else if kind == "foreign-volume" {
				volume := "Z:"
				if filepath.VolumeName(home) == volume {
					volume = "Y:"
				}
				indexedPath = `\\?\` + volume + `\outside.jsonl`
			} else if kind == "foreign-cwd" {
				codexFixtureMetadataCWD(t, path, `\\?\`+filepath.Join(home, "different-project"))
			} else {
				codexFixtureMetadataCWD(t, path, `\\.\C:\device`)
			}
			_, err := db.Exec("UPDATE threads SET rollout_path=?,cwd=? WHERE id=?", indexedPath, `\\?\`+cwd, codexConversationFixtureID)
			require.NoError(t, err)
			page, err := readCodexNativeConversationAt(context.Background(), agent.NativeConversationQuery{SessionID: codexConversationFixtureID}, home, index)
			require.Error(t, err)
			assert.Empty(t, page.Records)
			assert.NotContains(t, page.PartialReason, home)
		})
	}
}

func TestReadCodexNativeConversationWindowsRejectsMalformedAndNetworkPaths(t *testing.T) {
	for _, value := range []string{`\\?\UNC\server\share\history.jsonl`, `\\server\share\history.jsonl`,
		`\\.\C:\history.jsonl`, `\\?\GLOBALROOT\Device\HarddiskVolume1\history.jsonl`,
		`\\?\C:relative.jsonl`, `\\?\C:/history.jsonl`, `C:relative.jsonl`, `relative.jsonl`,
		`\\?\C:\history.jsonl:stream`, "\\\\?\\C:\\bad\x00name"} {
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
