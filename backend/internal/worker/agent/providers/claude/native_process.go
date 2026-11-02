package claude

import (
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func recognizeNativeProcess(name, executablePath string) leapmuxv1.NativeAgentProcessKind {
	if _, updated := claudeUpdatedExecutablePID(name); updated {
		var ok bool
		executablePath, ok = claudeUpdatedExecutablePath(name, executablePath)
		if !ok {
			return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED
		}
		name = "claude.exe"
	}
	if !strings.EqualFold(name, "claude") && !strings.EqualFold(name, "claude.exe") {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED
	}
	p := strings.ToLower(strings.ReplaceAll(executablePath, "\\", "/"))
	if strings.HasSuffix(p, "/claude.app/contents/macos/claude") ||
		(strings.Contains(p, "/windowsapps/claude_") && strings.HasSuffix(p, "/app/claude.exe")) {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP
	}
	if strings.HasSuffix(p, "/bin/claude") || strings.HasSuffix(p, "/bin/claude.exe") ||
		claudeHomebrewCaskExecutable(name, p) ||
		strings.Contains(p, "/.local/share/claude/versions/") ||
		(strings.Contains(p, "/claude-data/claude_") && strings.Contains(p, "/claude-code/") && strings.HasSuffix(p, "/claude.exe")) ||
		(strings.Contains(p, "/extensions/anthropic.claude-code-") && (strings.HasSuffix(p, "/resources/native-binary/claude.exe") || strings.HasSuffix(p, "/resources/native-binary/claude"))) {
		return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI
	}
	return leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN
}

// The Windows updater keeps a running CLI image under this exact name. The
// passive observer has no PID argument; native writes additionally bind the
// encoded PID to the process whose kernel identity was verified.
func claudeUpdatedExecutablePID(name string) (int, bool) {
	suffix, ok := strings.CutPrefix(strings.ToLower(name), "claude.exe.old.")
	if !ok {
		return 0, false
	}
	stamp, pidText, ok := strings.Cut(suffix, ".")
	if !ok || len(stamp) != 13 || stamp[0] == '0' || pidText == "" || pidText[0] == '0' {
		return 0, false
	}
	for _, digit := range stamp + pidText {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	pid, err := strconv.ParseInt(pidText, 10, 32)
	return int(pid), err == nil && pid > 0
}

func claudeUpdatedExecutablePath(name, executablePath string) (string, bool) {
	p := strings.ToLower(strings.ReplaceAll(executablePath, "\\", "/"))
	// Only absolute Windows CLI paths qualify. QueryFullProcessImageName may
	// retain claude.exe or expose the renamed image; both keep the same directory.
	if len(p) < 3 || p[0] < 'a' || p[0] > 'z' || p[1:3] != ":/" {
		return "", false
	}
	slash := strings.LastIndexByte(p, '/')
	base := p[slash+1:]
	if base != "claude.exe" && !strings.EqualFold(base, name) {
		return "", false
	}
	canonical := p[:slash+1] + "claude.exe"
	// Reuse the CLI path allowlist and explicitly exclude Desktop installations.
	if recognizeNativeProcess("claude.exe", canonical) != leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI {
		return "", false
	}
	return canonical, true
}
