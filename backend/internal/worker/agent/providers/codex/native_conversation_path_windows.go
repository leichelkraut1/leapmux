package codex

import "strings"

// codexNativeConversationPath maps Codex's verified verbatim LOCAL DRIVE
// spelling to an ordinary local-drive path. It never admits UNC/device paths,
// relative paths, alternate streams, or other extended namespaces. Scope,
// ownership, reparse and opened-file verification still run on the result.
func codexNativeConversationPath(value string) (string, bool) {
	if strings.HasPrefix(value, `\\?\`) {
		if len(value) < 7 || value[5] != ':' || value[6] != '\\' {
			return "", false
		}
		value = value[4:]
	}
	if len(value) < 3 || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) ||
		value[1] != ':' || (value[2] != '\\' && value[2] != '/') ||
		strings.ContainsRune(value, 0) || strings.ContainsRune(value[2:], ':') {
		return "", false
	}
	return value, true
}
