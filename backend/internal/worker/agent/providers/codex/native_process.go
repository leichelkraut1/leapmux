package codex

import (
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// recognizeNativeProcess never infers a session or turn from a process.
// Electron helpers and generic Node wrappers are deliberately not candidates.
func recognizeNativeProcess(name, executablePath string) leapmuxv1.NativeAgentProcessKind {
	if !strings.EqualFold(name, "codex") && !strings.EqualFold(name, "codex.exe") {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED
	}
	p := strings.ToLower(strings.ReplaceAll(executablePath, "\\", "/"))
	if strings.HasSuffix(p, "/codex.app/contents/macos/codex") ||
		(strings.Contains(p, "/windowsapps/openai.codex_") && strings.HasSuffix(p, "/app/codex.exe")) {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP
	}
	if strings.HasSuffix(p, "/chatgpt.app/contents/resources/codex-cli/codexcli.app/contents/macos/codex") ||
		strings.HasSuffix(p, "/bin/codex") || strings.HasSuffix(p, "/bin/codex.exe") ||
		strings.HasSuffix(p, "/codex/codex") || strings.HasSuffix(p, "/codex/codex.exe") ||
		(strings.Contains(p, "/openai/codex/bin/") && strings.HasSuffix(p, "/codex.exe")) ||
		(strings.Contains(p, "/extensions/openai.chatgpt-") && strings.Contains(p, "/bin/") && (strings.HasSuffix(p, "/codex.exe") || strings.HasSuffix(p, "/codex"))) {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI
	}
	return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN
}
