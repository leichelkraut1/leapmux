//go:build windows

package providerkit

import (
	"io/fs"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/leapmux/leapmux/util/useronly"
	"golang.org/x/sys/windows"
)

// Protect the directory at creation, before any attachment bytes can inherit
// permissions from it. Do not create broadly and restrict it afterward.
func mkdirPrivateAttachmentDir(path string) error {
	sddl, err := useronly.DirectorySDDL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	apiPath, err := attachmentWindowsAPIPath(path)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(apiPath)
	if err != nil {
		return err
	}
	attrs := &windows.SecurityAttributes{SecurityDescriptor: sd}
	attrs.Length = uint32(unsafe.Sizeof(*attrs))
	if err := windows.CreateDirectory(name, attrs); err != nil {
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

func checkPrivateAttachmentPath(path string, info fs.FileInfo) error {
	if info == nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errAttachmentStorageUnsafe
	}
	apiPath, err := attachmentWindowsAPIPath(path)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(apiPath)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	// Reject all reparse points, including junctions that may not look like a
	// symlink through a higher-level FileInfo.
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errAttachmentStorageUnsafe
	}
	owned, err := useronly.OwnedByCurrentUser(apiPath)
	if err != nil {
		return err
	}
	if !owned {
		return errAttachmentStorageUnsafe
	}
	sd, err := windows.GetNamedSecurityInfo(apiPath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	return checkPrivateAttachmentACL(sd, tokenUser.User.Sid)
}

// Direct Win32 calls do not receive os package long-path normalization. Keep
// short paths unchanged and extend absolute drive/UNC paths without depending
// on machine-wide LongPathsEnabled or an application manifest.
func attachmentWindowsAPIPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errAttachmentStorageUnsafe
	}
	path = filepath.Clean(path)
	if strings.HasPrefix(path, `\\.\`) {
		return "", errAttachmentStorageUnsafe
	}
	if len(path) < 248 || strings.HasPrefix(path, `\\?\`) {
		return path, nil
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:], nil
	}
	return `\\?\` + path, nil
}

// Ownership alone is insufficient. Refuse any other principal's allow ACE,
// including read access, and leave the existing ACL unchanged. Inherited owner
// permissions on the store's child files are valid.
func checkPrivateAttachmentACL(sd *windows.SECURITY_DESCRIPTOR, owner *windows.SID) error {
	if sd == nil || !sd.IsValid() || owner == nil {
		return errAttachmentStorageUnsafe
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 || dacl.AceCount > 64 {
		return errAttachmentStorageUnsafe
	}
	admitted := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return errAttachmentStorageUnsafe
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 {
			return errAttachmentStorageUnsafe
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !sid.Equals(owner) {
			return errAttachmentStorageUnsafe
		}
		admitted = true
	}
	if !admitted {
		return errAttachmentStorageUnsafe
	}
	return nil
}
