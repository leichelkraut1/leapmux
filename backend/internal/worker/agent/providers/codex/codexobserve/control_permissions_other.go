//go:build !unix && !windows

package codexobserve

import "github.com/leapmux/leapmux/internal/worker/agent"

func controlBaseDirectory(string) error        { return agent.ErrNativeControlUnavailable }
func controlMakePrivateDirectory(string) error { return agent.ErrNativeControlUnavailable }
func controlCheckPrivate(string) error         { return agent.ErrNativeControlUnavailable }
func controlSyncDirectory(string) error        { return agent.ErrNativeControlUnavailable }
