//go:build darwin

package codexobserve

import (
 "os"
 "path/filepath"
 "testing"

 "github.com/stretchr/testify/require"
)

func TestVerifyStatePathDarwin(t *testing.T) {
 directory,err:=filepath.EvalSymlinks(t.TempDir())
 require.NoError(t,err)
 require.NoError(t,os.Chmod(directory,0o700))
 file:=filepath.Join(directory,"metadata.sqlite")
 require.NoError(t,os.WriteFile(file,nil,0o600))
 require.NoError(t,VerifyStatePath(file))
 require.NoError(t,os.Chmod(file,0o666))
 require.Error(t,VerifyStatePath(file))
 require.NoError(t,os.Chmod(file,0o600))
 require.NoError(t,os.Chmod(directory,0o777))
 require.Error(t,VerifyStatePath(file))
 require.NoError(t,os.Chmod(directory,0o700))
 link:=filepath.Join(directory,"linked.sqlite")
 require.NoError(t,os.Symlink(file,link))
 require.Error(t,VerifyStatePath(link))
 require.Error(t,VerifyStatePath(directory))
}
