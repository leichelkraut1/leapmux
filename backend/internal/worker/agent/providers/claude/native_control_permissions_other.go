//go:build !unix && !windows

package claude

import (
	"context"
	"os"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func nativeControlPrivateDirectory(string) error      { return agent.ErrNativeControlUnavailable }
func nativeControlCheckPrivate(string) error          { return agent.ErrNativeControlUnavailable }
func nativeControlLock(string) (*os.File, error)      { return nil, agent.ErrNativeControlUnavailable }
func nativeControlSameUser(context.Context, int) bool { return false }
