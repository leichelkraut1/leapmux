//go:build darwin

package codexobserve

import (
 "context"
 "net"
 "os"
 "path/filepath"
 "syscall"
 "time"

 "github.com/shirou/gopsutil/v4/process"
 "golang.org/x/sys/unix"
)

func checkDarwinOwned(value string,mode os.FileMode) (os.FileInfo,error) {
 if noLinkedComponents(value)!=nil { return nil,errPeer }
 info,err:=os.Lstat(value)
 if err!=nil || info.Mode().Type()!=mode || info.Mode().Perm()&0o022!=0 { return nil,errPeer }
 stat,ok:=info.Sys().(*syscall.Stat_t)
 if !ok || stat.Uid!=uint32(os.Geteuid()) { return nil,errPeer }
 return info,nil
}

// VerifyStatePath admits only an existing regular file and same-user directory,
// neither writable by other users. It does not inspect or mutate DB contents.
func VerifyStatePath(value string) error {
 if _,err:=checkDarwinOwned(value,0); err!=nil { return errPeer }
 if _,err:=checkDarwinOwned(filepath.Dir(value),os.ModeDir); err!=nil { return errPeer }
 return nil
}

// Connect uses peer credentials from an existing Unix socket. No router,
// provider, helper process, session, or filesystem entry is created.
func Connect(ctx context.Context,codexHome string) (net.Conn,PeerIdentity,error) {
 if !filepath.IsAbs(codexHome) { return nil,PeerIdentity{},errPeer }
 directory:=filepath.Join(codexHome,"ipc")
 endpoint:=filepath.Join(directory,"ipc.sock")
 before,err:=checkDarwinOwned(endpoint,os.ModeSocket)
 if err!=nil { return nil,PeerIdentity{},errPeer }
 if _,err:=checkDarwinOwned(directory,os.ModeDir); err!=nil { return nil,PeerIdentity{},errPeer }
 if _,err:=checkDarwinOwned(codexHome,os.ModeDir); err!=nil { return nil,PeerIdentity{},errPeer }
 started:=time.Now().UnixNano()
 dialCtx,cancel:=context.WithTimeout(ctx,3*time.Second)
 defer cancel()
 raw,err:=(&net.Dialer{}).DialContext(dialCtx,"unix",endpoint)
 if err!=nil { return nil,PeerIdentity{},errPeer }
 good:=false
 defer func(){if !good { _=raw.Close() }}()
 conn,ok:=raw.(*net.UnixConn)
 if !ok { return nil,PeerIdentity{},errPeer }
 after,err:=checkDarwinOwned(endpoint,os.ModeSocket)
 if err!=nil || !os.SameFile(before,after) { return nil,PeerIdentity{},errPeer }
 peer,image,created,err:=readDarwinPeer(dialCtx,conn,started)
 if err!=nil { return nil,PeerIdentity{},errPeer }
 wrapped:=&verifiedConn{Conn:conn,peer:peer}
 wrapped.verify=func(checkCtx context.Context) error {
  current,err:=checkDarwinOwned(endpoint,os.ModeSocket)
  if err!=nil || !os.SameFile(before,current) { return errPeer }
  if _,err:=checkDarwinOwned(directory,os.ModeDir); err!=nil { return errPeer }
  next,nextImage,nextCreated,err:=readDarwinPeer(checkCtx,conn,started)
  if err!=nil || next!=peer || nextImage!=image || nextCreated!=created { return errPeer }
  return nil
 }
 if VerifyPeer(dialCtx,wrapped,peer)!=nil { return nil,PeerIdentity{},errPeer }
 good=true
 return wrapped,peer,nil
}

func readDarwinPeer(ctx context.Context,conn *net.UnixConn,connectedAt int64) (PeerIdentity,string,int64,error) {
 if ctx.Err()!=nil { return PeerIdentity{},"",0,errPeer }
 raw,err:=conn.SyscallConn()
 if err!=nil { return PeerIdentity{},"",0,errPeer }
 var pid int
 var credential *unix.Xucred
 var socketErr error
 err=raw.Control(func(fd uintptr){
  pid,socketErr=unix.GetsockoptInt(int(fd),unix.SOL_LOCAL,unix.LOCAL_PEERPID)
  if socketErr==nil { credential,socketErr=unix.GetsockoptXucred(int(fd),unix.SOL_LOCAL,unix.LOCAL_PEERCRED) }
 })
 if err!=nil || socketErr!=nil || pid<=0 || pid>0x7fffffff || credential==nil || credential.Uid!=uint32(os.Geteuid()) { return PeerIdentity{},"",0,errPeer }
 info,err:=unix.SysctlKinfoProc("kern.proc.pid",pid)
 if err!=nil || info.Eproc.Ucred.Uid!=uint32(os.Geteuid()) { return PeerIdentity{},"",0,errPeer }
 created:=int64(info.Proc.P_starttime.Sec)*int64(time.Second)+int64(info.Proc.P_starttime.Usec)*int64(time.Microsecond)
 if created<=0 || created>connectedAt { return PeerIdentity{},"",0,errPeer }
 // On Darwin ExeWithContext uses proc_pidpath, never arguments or environment.
 image,err:=(&process.Process{Pid:int32(pid)}).ExeWithContext(ctx)
 if err!=nil { return PeerIdentity{},"",0,errPeer }
 name,trusted:=darwinImageName(image)
 if !trusted || noLinkedComponents(image)!=nil { return PeerIdentity{},"",0,errPeer }
 again,err:=unix.SysctlKinfoProc("kern.proc.pid",pid)
 if err!=nil || again.Eproc.Ucred.Uid!=info.Eproc.Ucred.Uid ||
  again.Proc.P_starttime!=info.Proc.P_starttime || ctx.Err()!=nil { return PeerIdentity{},"",0,errPeer }
 return peerIdentity(int32(pid),created/int64(time.Millisecond),name),image,created,nil
}
