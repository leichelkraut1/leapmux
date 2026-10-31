package codex

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/codex/codexobserve"
)

func newNativeSessionController(options agent.NativeSessionControllerOptions) agent.NativeSessionController {
	return codexobserve.NewController(options)
}
