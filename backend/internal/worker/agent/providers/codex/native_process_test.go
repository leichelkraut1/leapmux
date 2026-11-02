package codex

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
)

func TestRecognizeNativeProcess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		kind       leapmuxv1.NativeAgentProcessKind
	}{
		{"Codex", "/Applications/Codex.app/Contents/MacOS/Codex", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP},
		{"Codex.exe", "C:/Program Files/WindowsApps/OpenAI.Codex_1/app/Codex.exe", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP},
		{"codex.exe", "C:/Users/example/AppData/Local/OpenAI/Codex/bin/1.0/codex.exe", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"codex", "/opt/homebrew/bin/codex", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"codex", "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"CODEX", "/Applications/CHATGPT.APP/Contents/Resources/CODEX-CLI/CodexCLI.app/Contents/MacOS/CODEX", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"ChatGPT", "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED},
		{"codex", "/Applications/Codex.app/Contents/Resources/codex", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN},
		{"codex", "/Applications/Other.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN},
		{"codex", "/unknown/custom/codex", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN},
		{"codex.exe", "C:/Users/example/.vscode/extensions/openai.chatgpt-1.0-win32-x64/bin/windows-x86_64/codex.exe", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI},
		{"codex.exe", "", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN},
		{"node", "/usr/bin/node", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED},
		{"Codex Helper", "/Applications/Codex.app/Contents/Frameworks/helper", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED},
		{"codex.cmd", "", leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED},
	} {
		t.Run(tc.name+tc.path, func(t *testing.T) { assert.Equal(t, tc.kind, recognizeNativeProcess(tc.name, tc.path)) })
	}
}
