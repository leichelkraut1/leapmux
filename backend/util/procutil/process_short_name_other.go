//go:build !darwin && !linux && !windows

package procutil

import (
	"context"
	"errors"

	"github.com/shirou/gopsutil/v4/process"
)

// An unreviewed name lookup must not silently fall back to reading arguments.
func shortProcessName(context.Context, *process.Process) (string, error) {
	return "", errors.New("safe process-name observation is unavailable on this OS")
}

func processUserKey(context.Context, *process.Process) (string, error) {
	return "", errors.New("safe process ownership observation is unavailable on this OS")
}
