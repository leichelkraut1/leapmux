//go:build windows

package procutil

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

// Windows uses the Toolhelp executable name or QueryFullProcessImageName,
// neither of which reads command arguments or environment.
func shortProcessName(ctx context.Context, candidate *process.Process) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return candidate.NameWithContext(ctx)
}

// Compare token SIDs directly. Account-name resolution can contact a domain
// controller and is unnecessary for a same-user comparison.
func processUserKey(ctx context.Context, candidate *process.Process) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(candidate.Pid))
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
