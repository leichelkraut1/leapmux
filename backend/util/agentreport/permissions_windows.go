//go:build windows

package agentreport

import (
 "io/fs"
 "unsafe"

 "github.com/leapmux/leapmux/util/useronly"
 "golang.org/x/sys/windows"
)

func mkdirPrivate(dir string) error {
 sddl, err := useronly.DirectorySDDL()
 if err != nil { return err }
 sd, err := windows.SecurityDescriptorFromString(sddl)
 if err != nil { return err }
 name, err := windows.UTF16PtrFromString(dir)
 if err != nil { return err }
 attrs := &windows.SecurityAttributes{SecurityDescriptor: sd}
 attrs.Length = uint32(unsafe.Sizeof(*attrs))
 if err := windows.CreateDirectory(name, attrs); err != nil {
  return &fs.PathError{Op: "mkdir", Path: dir, Err: err}
 }
 return nil
}

func restrictDirectory(dir string, info fs.FileInfo) error {
 owned, err := useronly.OwnedByCurrentUser(dir)
 if err != nil || !owned { return ErrUnavailable }
 _ = info
 sddl, err := useronly.DirectorySDDL()
 if err != nil { return err }
 sd, err := windows.SecurityDescriptorFromString(sddl)
 if err != nil { return err }
 dacl, _, err := sd.DACL()
 if err != nil { return err }
 return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
  windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
  nil, nil, dacl, nil)
}

func checkPrivate(path string, _ fs.FileInfo) error {
 owned, err := useronly.OwnedByCurrentUser(path)
 if err != nil || !owned { return ErrUnavailable }
 sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
 if err != nil { return ErrUnavailable }
 tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
 if err != nil { return ErrUnavailable }
 return checkPrivateACL(sd, tokenUser.User.Sid)
}

// Ownership alone is insufficient: another user may have an explicit write ACE.
// Accept only ordinary owner allow entries and deny entries; unknown ACE kinds
// fail closed. The inherited owner ACE on atomically written files is accepted.
func checkPrivateACL(sd *windows.SECURITY_DESCRIPTOR, owner *windows.SID) error {
 if sd == nil || !sd.IsValid() || owner == nil { return ErrUnavailable }
 dacl, _, err := sd.DACL()
 if err != nil || dacl == nil || dacl.AceCount == 0 || dacl.AceCount > 64 { return ErrUnavailable }
 admitted := false
 for i := uint32(0); i < uint32(dacl.AceCount); i++ {
  var ace *windows.ACCESS_ALLOWED_ACE
  if windows.GetAce(dacl, i, &ace) != nil || ace == nil { return ErrUnavailable }
  if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE { continue }
  if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 { return ErrUnavailable }
  sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
  if !sid.IsValid() || !sid.Equals(owner) { return ErrUnavailable }
  admitted = true
 }
 if !admitted { return ErrUnavailable }
 return nil
}
