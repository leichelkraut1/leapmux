package claude

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestRecognizeNativeProcess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		kind       leapmuxv1.NativeAgentProcessKind
	}{
		{"claude.exe", "C:/Users/example/.local/bin/claude.exe", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"claude", "/Users/example/.local/share/claude/versions/1.0", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"claude", "/opt/homebrew/bin/claude", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"Claude", "/Applications/Claude.app/Contents/MacOS/Claude", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP},
		{"Claude.exe", "C:/Program Files/WindowsApps/Claude_3.0/app/Claude.exe", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP},
		{"claude.exe", "C:/Users/example/AppData/Local/Claude-Data/claude_app/claude-code/2.0/hash/claude.exe", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"claude.exe", "C:/Users/example/.vscode/extensions/anthropic.claude-code-2.0-win32-x64/resources/native-binary/claude.exe", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"claude", "", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN},
		{"node", "/usr/bin/node", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED},
		{"claude-code-helper", "", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED},
	} {
		t.Run(tc.name+tc.path, func(t *testing.T) { assert.Equal(t, tc.kind, recognizeNativeProcess(tc.name, tc.path)) })
	}
}
