//go:build windows

package codexobserve

import (
 "context"
 "net"
 "os"
 "path/filepath"
 "time"
 "unsafe"

 "github.com/Microsoft/go-winio"
 "golang.org/x/sys/windows"
)

const codexPipe = "\\\\.\\pipe\\codex-ipc"

type windowsRoots struct { program,programX86,local string }

// Connect opens only the existing first-party pipe. go-winio uses anonymous
// impersonation level, so an untrusted endpoint cannot impersonate this client.
func Connect(ctx context.Context, _ string) (net.Conn,PeerIdentity,error) {
 started:=time.Now().UnixNano()
 dialCtx,cancel:=context.WithTimeout(ctx,3*time.Second)
 defer cancel()
 conn,err:=winio.DialPipeContext(dialCtx,codexPipe)
 if err!=nil { return nil,PeerIdentity{},errPeer }
 good:=false
 defer func(){if !good { _=conn.Close() }}()
 fd,ok:=conn.(interface{Fd() uintptr})
 if !ok { return nil,PeerIdentity{},errPeer }
 pipe:=windows.Handle(fd.Fd())
 var pid uint32
 if windows.GetNamedPipeServerProcessId(pipe,&pid)!=nil || pid==0 || pid>0x7fffffff { return nil,PeerIdentity{},errPeer }
 process,err:=windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION,false,pid)
 if err!=nil { return nil,PeerIdentity{},errPeer }
 defer func(){if !good { _=windows.CloseHandle(process) }}()
 roots:=windowsRoots{}
 roots.program,_=windows.KnownFolderPath(windows.FOLDERID_ProgramFiles,0)
 roots.programX86,_=windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX86,0)
 roots.local,_=windows.KnownFolderPath(windows.FOLDERID_LocalAppData,0)
 currentUser,err:=windows.GetCurrentProcessToken().GetTokenUser()
 if err!=nil { return nil,PeerIdentity{},errPeer }
 peer,image,created,err:=readWindowsPeer(dialCtx,pipe,process,pid,currentUser.User.Sid,roots,started)
 if err!=nil { return nil,PeerIdentity{},errPeer }
 wrapped:=&verifiedConn{Conn:conn,peer:peer,release:func(){_=windows.CloseHandle(process)}}
 wrapped.verify=func(checkCtx context.Context) error {
  next,nextImage,nextCreated,err:=readWindowsPeer(checkCtx,pipe,process,pid,currentUser.User.Sid,roots,started)
  if err!=nil || next!=peer || nextImage!=image || nextCreated!=created { return errPeer }
  return nil
 }
 if err:=VerifyPeer(dialCtx,wrapped,peer); err!=nil { return nil,PeerIdentity{},errPeer }
 good=true
 return wrapped,peer,nil
}

func readWindowsPeer(ctx context.Context,pipe,process windows.Handle,pid uint32,owner *windows.SID,roots windowsRoots,connectedAt int64) (PeerIdentity,string,int64,error) {
 if ctx.Err()!=nil { return PeerIdentity{},"",0,errPeer }
 var actualPID,exit uint32
 if windows.GetNamedPipeServerProcessId(pipe,&actualPID)!=nil || actualPID!=pid ||
  windows.GetExitCodeProcess(process,&exit)!=nil || exit!=259 { return PeerIdentity{},"",0,errPeer }
 var token windows.Token
 if windows.OpenProcessToken(process,windows.TOKEN_QUERY,&token)!=nil { return PeerIdentity{},"",0,errPeer }
 user,err:=token.GetTokenUser()
 _=token.Close()
 if err!=nil || !user.User.Sid.Equals(owner) { return PeerIdentity{},"",0,errPeer }
 var creation,exited,kernel,cpu windows.Filetime
 if windows.GetProcessTimes(process,&creation,&exited,&kernel,&cpu)!=nil { return PeerIdentity{},"",0,errPeer }
 created:=creation.Nanoseconds()
 if created<=0 || created>connectedAt { return PeerIdentity{},"",0,errPeer }
 buffer:=make([]uint16,32768)
 size:=uint32(len(buffer))
 if windows.QueryFullProcessImageName(process,0,&buffer[0],&size)!=nil || size==0 || size>=uint32(len(buffer)) { return PeerIdentity{},"",0,errPeer }
 image:=windows.UTF16ToString(buffer[:size])
 name,trusted:=windowsImageName(image,roots.program,roots.programX86,roots.local)
 if !trusted || noWindowsLinks(image)!=nil || ctx.Err()!=nil { return PeerIdentity{},"",0,errPeer }
 return peerIdentity(int32(pid),created/int64(time.Millisecond),name),image,created,nil
}

// VerifyStatePath checks the file and its directory without changing their ACLs.
// Other users may not write either. SYSTEM and Administrators remain trusted OS
// principals, as with the user's normal private profile directories.
func noWindowsLinks(value string) error {
 if noLinkedComponents(value)!=nil { return errPeer }
 for current:=filepath.Clean(value);; {
  encoded,err:=windows.UTF16PtrFromString(current)
  if err!=nil { return errPeer }
  attrs,err:=windows.GetFileAttributes(encoded)
  if err!=nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT!=0 { return errPeer }
  parent:=filepath.Dir(current)
  if parent==current { break }
  current=parent
 }
 return nil
}

func VerifyStatePath(value string) error {
 if noWindowsLinks(value)!=nil { return errPeer }
 info,err:=os.Lstat(value)
 if err!=nil || !info.Mode().IsRegular() { return errPeer }
 user,err:=windows.GetCurrentProcessToken().GetTokenUser()
 if err!=nil { return errPeer }
 for _,item:=range []string{value,filepath.Dir(value)} {
  sd,err:=windows.GetNamedSecurityInfo(item,windows.SE_FILE_OBJECT,windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
  if err!=nil || verifyWindowsACL(sd,user.User.Sid)!=nil { return errPeer }
 }
 return nil
}

func verifyWindowsACL(sd *windows.SECURITY_DESCRIPTOR,user *windows.SID) error {
 if sd==nil || !sd.IsValid() || user==nil { return errPeer }
 owner,_,err:=sd.Owner()
 if err!=nil || owner==nil || !owner.Equals(user) { return errPeer }
 acl,_,err:=sd.DACL()
 if err!=nil || acl==nil || acl.AceCount==0 || acl.AceCount>128 { return errPeer }
 // FILE_DELETE_CHILD is defined by WinNT.h but absent from this x/sys version.
 // https://learn.microsoft.com/en-us/windows/win32/fileio/file-access-rights-constants
 const fileDeleteChild = 0x0040
 const writable=windows.GENERIC_ALL|windows.GENERIC_WRITE|windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|
  windows.FILE_WRITE_EA|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|fileDeleteChild
 admitted:=false
 for i:=uint32(0);i<uint32(acl.AceCount);i++ {
  var ace *windows.ACCESS_ALLOWED_ACE
  if windows.GetAce(acl,i,&ace)!=nil || ace==nil { return errPeer }
  if ace.Header.AceType==windows.ACCESS_DENIED_ACE_TYPE { continue }
  if ace.Header.AceType!=windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize<16 { return errPeer }
  sid:=(*windows.SID)(unsafe.Pointer(&ace.SidStart))
  if !sid.IsValid() { return errPeer }
  if sid.Equals(user) { admitted=true;continue }
  key:=sid.String()
  if key=="S-1-5-18" || key=="S-1-5-32-544" { continue }
  if ace.Mask&writable!=0 { return errPeer }
 }
 if !admitted { return errPeer }
 return nil
}
