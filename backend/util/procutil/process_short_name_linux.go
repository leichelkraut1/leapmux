//go:build linux

package procutil

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// Read comm only. gopsutil's long-name fallback reads /proc/PID/cmdline.
func shortProcessName(ctx context.Context, candidate *process.Process) (string, error) {
	return readProcShortName(ctx, "/proc", int(candidate.Pid))
}

func readProcShortName(ctx context.Context, procRoot string, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	// TASK_COMM_LEN is 16 including NUL; the proc file adds a newline.
	name, err := io.ReadAll(io.LimitReader(file, 17))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(name), "\n"), nil
}

// Ownership comes only from the kernel UID line. gopsutil Username/Uids also
// parse Name and can expand a long name by reading command arguments.
func processUserKey(ctx context.Context, candidate *process.Process) (string, error) {
	return readProcUserKey(ctx, "/proc", int(candidate.Pid))
}

func readProcUserKey(ctx context.Context, procRoot string, pid int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(io.LimitReader(file, 64*1024))
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "Uid:" {
			continue
		}
		// real, effective, saved, filesystem UID: compare effective OS users.
		if len(fields) != 5 {
			return "", errors.New("invalid process ownership metadata")
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return "", errors.New("invalid process ownership metadata")
		}
		return strconv.FormatUint(uid, 10), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("process ownership metadata unavailable")
}
