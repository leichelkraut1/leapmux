package bootstrap

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/providers"
)

// NativeProcessRecognizer binds executable-metadata recognition to the same
// provider registry as Wire. It constructs the registry once for the caller;
// recognizing metadata never starts an agent or a native-service observer.
func NativeProcessRecognizer() func(name, executablePath string) (leapmuxv1.AgentProvider, leapmuxv1.NativeAgentProcessKind) {
	return providers.Registry().RecognizeNativeProcess
}
