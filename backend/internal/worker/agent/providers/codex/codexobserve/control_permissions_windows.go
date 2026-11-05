//go:build windows

package codexobserve

import (
	"os"
	"unsafe"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/useronly"
	"golang.org/x/sys/windows"
)

func controlBaseDirectory(value string) error {
	if noWindowsLinks(value) != nil {
		return agent.ErrNativeControlUnavailable
	}
	info, err := os.Lstat(value)
	if err != nil || !info.IsDir() {
		return agent.ErrNativeControlUnavailable
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	sd, err := windows.GetNamedSecurityInfo(value, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || verifyWindowsACL(sd, user.User.Sid) != nil {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

func controlMakePrivateDirectory(value string) error {
	sddl, err := useronly.DirectorySDDL()
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	name, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	attributes := &windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	if err := windows.CreateDirectory(name, attributes); err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return agent.ErrNativeControlUnavailable
	}
	return controlCheckPrivate(value)
}

func controlCheckPrivate(value string) error {
	if noWindowsLinks(value) != nil {
		return agent.ErrNativeControlUnavailable
	}
	sd, err := windows.GetNamedSecurityInfo(value, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return agent.ErrNativeControlUnavailable
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return agent.ErrNativeControlUnavailable
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 || acl.AceCount > 128 {
		return agent.ErrNativeControlUnavailable
	}
	admitted := false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil {
			return agent.ErrNativeControlUnavailable
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 {
			return agent.ErrNativeControlUnavailable
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !sid.Equals(user.User.Sid) {
			return agent.ErrNativeControlUnavailable
		}
		admitted = true
	}
	if !admitted {
		return agent.ErrNativeControlUnavailable
	}
	return nil
}

// File.Sync flushes the claim before transport writes. Windows directory
// handles do not support the POSIX directory-fsync operation used on Unix;
// the journal's Windows guarantee covers process failure/restart, not power loss.
func controlSyncDirectory(string) error { return nil }
