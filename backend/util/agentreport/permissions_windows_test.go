//go:build windows

package agentreport

import (
 "context"
 "os"
 "path/filepath"
 "testing"
 "time"

 "github.com/leapmux/leapmux/util/useronly"
 "github.com/stretchr/testify/assert"
 "github.com/stretchr/testify/require"
 "golang.org/x/sys/windows"
)

func TestPrivateACLRejectsOtherUsersAndMissingRestrictions(t *testing.T) {
 t.Parallel()
 sidString, err := useronly.CurrentUserSID()
 require.NoError(t, err)
 sid, err := windows.StringToSid(sidString)
 require.NoError(t, err)
 for _, tc := range []struct { name, sddl string; accepted bool }{
  {"owner", "D:P(A;OICI;FA;;;" + sidString + ")", true},
  {"inherited owner", "D:(A;ID;FA;;;" + sidString + ")", true},
  {"owner and deny", "D:P(D;;FW;;;BU)(A;;FA;;;" + sidString + ")", true},
  {"other user write", "D:P(A;;FA;;;" + sidString + ")(A;;FW;;;BU)", false},
  {"everyone read", "D:P(A;;FA;;;" + sidString + ")(A;;FR;;;WD)", false},
  {"null ACL", "D:NO_ACCESS_CONTROL", false},
  {"empty ACL", "D:", false},
  {"absent ACL", "O:" + sidString, false},
  {"deny only", "D:P(D;;FW;;;BU)", false},
 } {
  t.Run(tc.name, func(t *testing.T) {
   sd, err := windows.SecurityDescriptorFromString(tc.sddl)
   require.NoError(t, err)
   err = checkPrivateACL(sd, sid)
   if tc.accepted { require.NoError(t, err) } else { require.ErrorIs(t, err, ErrUnavailable) }
  })
 }
 assert.ErrorIs(t, checkPrivateACL(nil, sid), ErrUnavailable)
}

// These fixtures alter only disposable test paths. A current-user owner cannot
// make a broadly writable report trustworthy by itself.
func TestReadRejectsBroadWindowsACLsWithoutChangingOwnership(t *testing.T) {
 t.Parallel()
 dir := reportDirectory(t)
 now := time.Now().UTC()
 report := fixtureReport(now)
 require.NoError(t, Write(dir, report))
 path := filepath.Join(dir, reportName(report.SessionID))
 broadenTestACL(t, path)
 owned, err := useronly.OwnedByCurrentUser(path)
 require.NoError(t, err)
 require.True(t, owned)
 rows, err := Read(context.Background(), dir, now)
 require.NoError(t, err)
 assert.Empty(t, rows)
 assert.ErrorIs(t, Write(dir, report), ErrUnavailable)
 require.NoError(t, os.Remove(path))
 broadenTestACL(t, dir)
 _, err = Read(context.Background(), dir, now)
 assert.ErrorIs(t, err, ErrUnavailable)
 // Write may make its owned directory private before creating a new report.
 require.NoError(t, Write(dir, report))
 rows, err = Read(context.Background(), dir, now)
 require.NoError(t, err)
 require.Len(t, rows, 1)
}

func broadenTestACL(t *testing.T, path string) {
 t.Helper()
 sid, err := useronly.CurrentUserSID()
 require.NoError(t, err)
 sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FW;;;BU)")
 require.NoError(t, err)
 dacl, _, err := sd.DACL()
 require.NoError(t, err)
 require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
  windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
  nil, nil, dacl, nil))
}
