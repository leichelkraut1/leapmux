package claude

import "strings"

// Homebrew resolves its bin symlink to this versioned Cask artifact. Native
// process identity exposes the artifact path, rather than the bin symlink.
func claudeHomebrewCaskExecutable(name, executablePath string) bool {
	if !strings.EqualFold(name, "claude") {
		return false
	}
	var artifact string
	for _, prefix := range []string{"/opt/homebrew/caskroom/claude-code/", "/usr/local/caskroom/claude-code/"} {
		if rest, ok := strings.CutPrefix(executablePath, prefix); ok {
			artifact = rest
			break
		}
	}
	version, filename, ok := strings.Cut(artifact, "/")
	if !ok || filename != "claude" || len(version) > 64 {
		return false
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}
