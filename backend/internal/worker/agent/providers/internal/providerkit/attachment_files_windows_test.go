//go:build windows

package providerkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/useronly"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestAttachmentPrivateACL(t *testing.T) {
	t.Parallel()
	sidText, err := useronly.CurrentUserSID()
	require.NoError(t, err)
	sid, err := windows.StringToSid(sidText)
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		sddl     string
		accepted bool
	}{
		{"owner only", "D:P(A;OICI;FA;;;" + sidText + ")", true},
		{"inherited owner", "D:(A;ID;FA;;;" + sidText + ")", true},
		{"owner and deny", "D:P(D;;FW;;;BU)(A;;FA;;;" + sidText + ")", true},
		{"other user write", "D:P(A;;FA;;;" + sidText + ")(A;;FW;;;BU)", false},
		{"everyone read", "D:P(A;;FA;;;" + sidText + ")(A;;FR;;;WD)", false},
		{"null ACL", "D:NO_ACCESS_CONTROL", false},
		{"empty ACL", "D:", false},
		{"absent ACL", "O:" + sidText, false},
		{"deny only", "D:P(D;;FW;;;BU)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			require.NoError(t, err)
			err = checkPrivateAttachmentACL(sd, sid)
			if tc.accepted {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errAttachmentStorageUnsafe)
			}
		})
	}
	assert.ErrorIs(t, checkPrivateAttachmentACL(nil, sid), errAttachmentStorageUnsafe)
}

func TestAttachmentPrivateWindowsDirectoryAndInheritedFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "attachments")
	require.NoError(t, mkdirPrivateAttachmentDir(dir))
	info, err := os.Lstat(dir)
	require.NoError(t, err)
	require.NoError(t, checkPrivateAttachmentPath(dir, info))
	path := filepath.Join(dir, "document.bin")
	require.NoError(t, os.WriteFile(path, []byte{0, 1, 2}, 0o600))
	info, err = os.Lstat(path)
	require.NoError(t, err)
	require.NoError(t, checkPrivateAttachmentPath(path, info))

	sid, err := useronly.CurrentUserSID()
	require.NoError(t, err)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)")
	require.NoError(t, err)
	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil))
	owned, err := useronly.OwnedByCurrentUser(path)
	require.NoError(t, err)
	require.True(t, owned, "ownership alone must not admit a broad attachment")
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	info, err = os.Lstat(path)
	require.NoError(t, err)
	require.ErrorIs(t, checkPrivateAttachmentPath(path, info), errAttachmentStorageUnsafe)
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	assert.Equal(t, before.String(), after.String(), "validation must not rewrite an existing ACL")
	require.ErrorIs(t, checkPrivateAttachmentPath(path, nil), errAttachmentStorageUnsafe)
}

func TestAttachmentWindowsAPIPath(t *testing.T) {
	t.Parallel()
	longPart := strings.Repeat("a", 240)
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"short drive", `C:\worker\attachments`, `C:\worker\attachments`},
		{"short UNC", `\\server\share\attachments`, `\\server\share\attachments`},
		{"long drive", `C:\worker\` + longPart, `\\?\C:\worker\` + longPart},
		{"long UNC", `\\server\share\` + longPart, `\\?\UNC\server\share\` + longPart},
		{"extended drive", `\\?\C:\worker\` + longPart, `\\?\C:\worker\` + longPart},
		{"extended UNC", `\\?\UNC\server\share\` + longPart, `\\?\UNC\server\share\` + longPart},
		{"forward separators", `C:/worker/` + longPart, `\\?\C:\worker\` + longPart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := attachmentWindowsAPIPath(tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	for _, path := range []string{"", `attachments\chat`, `C:attachments`, `\attachments`, `\\.\pipe\attachment`} {
		_, err := attachmentWindowsAPIPath(path)
		require.ErrorIs(t, err, errAttachmentStorageUnsafe)
	}
}

func TestStageFileAttachmentWindowsLongPath(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	for len(base) <= 280 {
		base = filepath.Join(base, strings.Repeat("a", 60))
	}
	require.NoError(t, os.MkdirAll(base, 0o700))
	dir := filepath.Join(base, "attachments", "chat")
	attachment := agent.ClassifiedAttachment{
		Filename: "long-path.ACD",
		MIMEType: "application/octet-stream",
		Data:     []byte{0, 255, 1, 254},
		Kind:     agent.AttachmentKindBinary,
	}
	first, err := StageFileAttachment(dir, attachment)
	require.NoError(t, err)
	require.Greater(t, len(first.Path), 260)
	data, err := os.ReadFile(first.Path)
	require.NoError(t, err)
	assert.Equal(t, attachment.Data, data)
	second, err := StageFileAttachment(dir, attachment)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "long-path publication leaves only the verified attachment")
}
