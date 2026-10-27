package claude

import (
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func recognizeNativeProcess(name, executablePath string) leapmuxv1.NativeAgentProcessKind {
	if !strings.EqualFold(name, "claude") && !strings.EqualFold(name, "claude.exe") {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED
	}
	p := strings.ToLower(strings.ReplaceAll(executablePath, "\\", "/"))
	if strings.HasSuffix(p, "/claude.app/contents/macos/claude") ||
		(strings.Contains(p, "/windowsapps/claude_") && strings.HasSuffix(p, "/app/claude.exe")) {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP
	}
	if strings.HasSuffix(p, "/bin/claude") || strings.HasSuffix(p, "/bin/claude.exe") ||
		strings.Contains(p, "/.local/share/claude/versions/") ||
		(strings.Contains(p, "/claude-data/claude_") && strings.Contains(p, "/claude-code/") && strings.HasSuffix(p, "/claude.exe")) ||
		(strings.Contains(p, "/extensions/anthropic.claude-code-") && (strings.HasSuffix(p, "/resources/native-binary/claude.exe") || strings.HasSuffix(p, "/resources/native-binary/claude"))) {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI
	}
	return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN
}
