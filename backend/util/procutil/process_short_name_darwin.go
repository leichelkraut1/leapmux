//go:build darwin

package procutil

import (
	"context"
	"strconv"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

// Use only kernel process metadata. gopsutil NameWithContext expands long
// Darwin names through kern.procargs2, which reads arguments and environment.
func shortProcessName(ctx context.Context, candidate *process.Process) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", int(candidate.Pid))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return shortNameFromKinfo(info), nil
}

func shortNameFromKinfo(info *unix.KinfoProc) string {
	return unix.ByteSliceToString(info.Proc.P_comm[:])
}

func processUserKey(ctx context.Context, candidate *process.Process) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", int(candidate.Pid))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(info.Eproc.Ucred.Uid), 10), nil
}
