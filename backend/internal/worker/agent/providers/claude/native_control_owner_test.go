package claude

import (
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
)

func TestNativeControlUpdatedExecutableIdentity(t *testing.T) {
	t.Parallel()
	const updated = "claude.exe.old.1793418567112.12184"
	for _, tc := range []struct {
		label, name, path string
		pid               int
		accepted          bool
	}{
		{"canonical image query", updated, "C:/Users/generated/.local/bin/claude.exe", 12184, true},
		{"renamed image query", updated, "C:/Users/generated/.local/bin/" + updated, 12184, true},
		{"case insensitive Windows", strings.ToUpper(updated), "C:\\Users\\generated\\.local\\bin\\" + strings.ToUpper(updated), 12184, true},
		{"extension CLI", updated, "C:/Users/generated/.vscode/extensions/anthropic.claude-code-2.1/resources/native-binary/" + updated, 12184, true},
		{"wrong live PID", updated, "C:/Users/generated/.local/bin/claude.exe", 12185, false},
		{"different image PID", updated, "C:/Users/generated/.local/bin/claude.exe.old.1793418567112.12185", 12184, false},
		{"different image stamp", updated, "C:/Users/generated/.local/bin/claude.exe.old.1793418567113.12184", 12184, false},
		{"Desktop image", updated, "C:/Program Files/WindowsApps/Claude_3.0/app/claude.exe", 12184, false},
		{"different application", updated, "C:/Users/generated/.local/bin/not-claude.exe", 12184, false},
		{"unrecognized directory", updated, "C:/unrelated/" + updated, 12184, false},
		{"POSIX image", updated, "/usr/bin/claude", 12184, false},
		{"relative image", updated, "bin/claude.exe", 12184, false},
		{"short timestamp", "claude.exe.old.179341856711.12184", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"long timestamp", "claude.exe.old.17934185671120.12184", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"nonnumeric timestamp", "claude.exe.old.179341856711x.12184", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"nonnumeric PID", "claude.exe.old.1793418567112.12184x", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"zero PID", "claude.exe.old.1793418567112.0", "C:/Users/generated/.local/bin/claude.exe", 0, false},
		{"overflow PID", "claude.exe.old.1793418567112.2147483648", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"leading zero PID", "claude.exe.old.1793418567112.012184", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"extra suffix", updated + ".exe", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"generic prefix", "claude-helper.exe", "C:/Users/generated/.local/bin/claude.exe", 12184, false},
		{"normal CLI unchanged", "claude.exe", "C:/Users/generated/.local/bin/claude.exe", 12184, true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			assert.Equal(t, tc.accepted, claudeNativeControlExecutable(tc.pid, tc.name, tc.path))
			if tc.accepted {
				assert.Equal(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI, recognizeNativeProcess(tc.name, tc.path))
			}
		})
	}
}

func TestRecognizeUpdatedNativeProcessWithoutLivePID(t *testing.T) {
	t.Parallel()
	const updated = "claude.exe.old.1793418567112.12184"
	assert.Equal(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI,
		recognizeNativeProcess(updated, "C:/Users/generated/.local/bin/claude.exe"))
	assert.Equal(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED,
		recognizeNativeProcess(updated, "C:/Program Files/WindowsApps/Claude_3.0/app/claude.exe"))
}
