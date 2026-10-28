package codex

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex/codexobserve"
)

func newNativeServiceObserver() agent.NativeServiceObserver {
	return codexobserve.NewWatcher()
}
