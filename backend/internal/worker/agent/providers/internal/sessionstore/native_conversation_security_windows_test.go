//go:build windows

package sessionstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestNativeConversationWindowsRejectsUNCDeviceAndAlternateStreamRoots(t *testing.T) {
	for _, value := range []string{"\\\\server\\share\\codex", "\\\\?\\C:\\codex", "\\\\.\\C:\\codex", "C:\\codex:stream"} {
		assert.False(t, nativeRootAllowed(value))
	}
	assert.True(t, nativeRootAllowed("C:\\Users\\fixture\\.codex"))
}

func TestNativeConversationWindowsReadsOwnedLongPathThroughHandleChecks(t *testing.T) {
	q, oldPath := nativeConversationFixture(t, 1)
	relative := filepath.Join("sessions", strings.Repeat("a", 140), strings.Repeat("b", 140), "harmless.jsonl")
	path := filepath.Join(q.Root, relative)
	require.Greater(t, len(path), 260)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	data, err := os.ReadFile(oldPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	q.RelativePath = relative
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
}

func TestNativeConversationWindowsRejectsForeignWritablePathAndHandle(t *testing.T) {
	q, path := nativeConversationFixture(t, 1)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	privateDACL := "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	// This fixture must not inherit workspace grants to sandbox or other users.
	// Apply the private DACL to every generated component, including children
	// created before protection of the temporary root.
	for _, item := range []string{q.Root, filepath.Dir(path), path} {
		setNativeConversationWindowsFixtureDACL(t, item, privateDACL)
	}
	t.Cleanup(func() { setNativeConversationWindowsFixtureDACL(t, path, privateDACL) })

	require.NoError(t, VerifyNativeConversationFilePath(q.Root, q.RelativePath))
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	file, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, file.Close()) })
	require.NoError(t, nativeOpenedFileOwned(file))

	// BUILTIN\Users can now write this file. Ownership and the current user's
	// own FullControl ACE are unchanged, so ownership alone cannot admit it.
	setNativeConversationWindowsFixtureDACL(t, path, privateDACL+"(A;;FW;;;BU)")
	before, err := windows.GetNamedSecurityInfo(nativeWindowsAPIPath(path), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	require.ErrorIs(t, VerifyNativeConversationFilePath(q.Root, q.RelativePath), ErrNativeConversationUnsafe)
	require.ErrorIs(t, nativeOpenedFileOwned(file), ErrNativeConversationUnsafe,
		"an already-open handle must observe the effective shared-write DACL too")
	_, err = ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationUnsafe)

	// Refusal must not repair or modify a provider-owned file's permissions.
	after, err := windows.GetNamedSecurityInfo(nativeWindowsAPIPath(path), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	assert.Equal(t, before.String(), after.String())
}

func setNativeConversationWindowsFixtureDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	require.NoError(t, err)
	acl, _, err := descriptor.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(nativeWindowsAPIPath(path), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil))
}
