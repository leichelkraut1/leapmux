//go:build windows

package claude

import (
	"context"
	"os"
	"unsafe"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/useronly"
	"golang.org/x/sys/windows"
)

func nativeControlPrivateDirectory(path string) error {
	sddl, err := useronly.DirectorySDDL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	if err := windows.CreateDirectory(name, attributes); err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return agent.ErrNativeControlUnavailable
	}
	return nativeControlCheckPrivate(path)
}

func nativeControlCheckPrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return agent.ErrNativeControlUnavailable
	}
	owned, err := useronly.OwnedByCurrentUser(path)
	if err != nil || !owned {
		return agent.ErrNativeControlUnavailable
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 || dacl.AceCount > 64 {
		return agent.ErrNativeControlUnavailable
	}
	admitted := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, index, &ace) != nil || ace == nil {
			return agent.ErrNativeControlUnavailable
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 {
			return agent.ErrNativeControlUnavailable
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !sid.Equals(tokenUser.User.Sid) {
			return agent.ErrNativeControlUnavailable
		}
		admitted = true
	}
	if !admitted {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

func nativeControlLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := nativeControlCheckPrivate(path); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func nativeControlSameUser(_ context.Context, pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var token windows.Token
	if windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token) != nil {
		return false
	}
	defer func() { _ = token.Close() }()
	other, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && other.User.Sid.Equals(current.User.Sid)
}
