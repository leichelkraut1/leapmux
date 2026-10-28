package bootstrap

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
)

func TestNativeProcessRecognizerUsesTheComposedProviders(t *testing.T) {
	t.Parallel()
	recognize := NativeProcessRecognizer()
	for _, tc := range []struct {
		name     string
		path     string
		provider leapmuxv1.AgentProvider
		kind     leapmuxv1.NativeAgentProcessKind
	}{
		{
			name: "codex.exe", path: "C:/Users/test/AppData/Local/OpenAI/Codex/bin/build/codex.exe",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			kind:     leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI,
		},
		{
			name: "codex", path: "/Applications/Codex.app/Contents/MacOS/Codex",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			kind:     leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP,
		},
		{
			name:     "codex",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			kind:     leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN,
		},
		{
			name: "claude", path: "/home/test/.local/bin/claude",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			kind:     leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI,
		},
		{
			name: "unrelated", path: "/usr/bin/unrelated",
			provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED,
			kind:     leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNSPECIFIED,
		},
	} {
		t.Run(tc.name+"/"+tc.path, func(t *testing.T) {
			provider, kind := recognize(tc.name, tc.path)
			assert.Equal(t, tc.provider, provider)
			assert.Equal(t, tc.kind, kind)
		})
	}
}
