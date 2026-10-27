//go:build windows

package procutil

import (
	"context"
	"errors"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

// OpenProcess reports an exited PID as INVALID_PARAMETER. Confirm absence;
// permission failures and failed existence checks are not evidence of exit.
func platformObservationGone(ctx context.Context, pid int, err error) bool {
	return windowsObservationGone(err, func() (bool, error) {
		return process.PidExistsWithContext(ctx, int32(pid))
	})
}

func windowsObservationGone(err error, exists func() (bool, error)) bool {
	if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false
	}
	present, checkErr := exists()
	return checkErr == nil && !present
}
