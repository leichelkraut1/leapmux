//go:build windows

package sessionstore

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func nativeReparsePoint(value string) bool {
	if !nativeRootAllowed(value) {
		return true
	}
	encoded, err := windows.UTF16PtrFromString(nativeWindowsAPIPath(value))
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(encoded)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func nativeRootAllowed(value string) bool {
	// Configured roots are ordinary local drive paths. Extended paths are
	// produced only internally for APIs that do not expand long paths.
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/') && !strings.ContainsRune(value, 0) && !strings.ContainsRune(value[2:], ':')
}

func nativeWindowsAPIPath(value string) string {
	return "\\\\?\\" + strings.ReplaceAll(value, "/", "\\")
}

func nativeOwned(value string, directory bool) (os.FileInfo, error) {
	info, err := os.Lstat(value)
	if err != nil {
		return nil, err
	}
	if nativeReparsePoint(value) || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return nil, ErrNativeConversationUnsafe
	}
	descriptor, err := windows.GetNamedSecurityInfo(nativeWindowsAPIPath(value), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return nil, ErrNativeConversationUnsafe
	}
	if err := nativeWindowsDescriptorOwned(descriptor); err != nil {
		return nil, err
	}
	return info, nil
}

func nativeWindowsDescriptorOwned(descriptor *windows.SECURITY_DESCRIPTOR) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return ErrNativeConversationUnsafe
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return ErrNativeConversationUnsafe
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 || acl.AceCount > 128 {
		return ErrNativeConversationUnsafe
	}
	const writable = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
		windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | 0x0040
	admitted := false
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, index, &ace) != nil || ace == nil {
			return ErrNativeConversationUnsafe
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 {
			return ErrNativeConversationUnsafe
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() {
			return ErrNativeConversationUnsafe
		}
		if sid.Equals(user.User.Sid) {
			admitted = true
			continue
		}
		if key := sid.String(); key != "S-1-5-18" && key != "S-1-5-32-544" && ace.Mask&writable != 0 {
			return ErrNativeConversationUnsafe
		}
	}
	if !admitted {
		return ErrNativeConversationUnsafe
	}
	return nil
}

func nativeOpenedFileOwned(file *os.File) error {
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return ErrNativeConversationUnsafe
	}
	return nativeWindowsDescriptorOwned(descriptor)
}

func nativeFileIdentity(file *os.File) (string, error) {
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info) != nil {
		return "", ErrNativeConversationUnsafe
	}
	return fmt.Sprintf("%d:%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, info.CreationTime.Nanoseconds()), nil
}
