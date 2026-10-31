//go:build !windows

package codex

// Non-Windows providers retain their existing native path interpretation.
func codexNativeConversationPath(value string) (string, bool) {
	return value, true
}
