package claude

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func TestRecognizeHomebrewCaskClaude(t *testing.T) {
	for _, executable := range []string{
		"/opt/homebrew/Caskroom/claude-code/2.1.308/claude",
		"/usr/local/Caskroom/claude-code/2.1.314/claude",
	} {
		t.Run(executable, func(t *testing.T) {
			require.Equal(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI,
				recognizeNativeProcess("claude", executable))
		})
	}
	for _, executable := range []string{
		"opt/homebrew/Caskroom/claude-code/2.1.308/claude",
		"/tmp/opt/homebrew/Caskroom/claude-code/2.1.308/claude",
		"/opt/homebrew/Caskroom/other/2.1.308/claude",
		"/opt/homebrew/Caskroom/claude-code/2.1/claude",
		"/opt/homebrew/Caskroom/claude-code/latest/claude",
		"/opt/homebrew/Caskroom/claude-code/2.1.308/../claude",
		"/opt/homebrew/Caskroom/claude-code/2.1.308/nested/claude",
		"/opt/homebrew/Caskroom/claude-code/2.1.308/claude.exe",
		"/opt/homebrew/Caskroom/claude-code/2.1.308/claude/",
		"/opt/homebrew/Caskroom/claude-code//claude",
		"/opt/homebrew/Caskroom/claude-code/2..308/claude",
		"/opt/homebrew/Caskroom/claude-code/2.1.308-beta/claude",
	} {
		t.Run(executable, func(t *testing.T) {
			require.NotEqual(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI,
				recognizeNativeProcess("claude", executable))
		})
	}
	for _, name := range []string{"other", "claude.exe"} {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI,
				recognizeNativeProcess(name, "/opt/homebrew/Caskroom/claude-code/2.1.308/claude"))
		})
	}
}
