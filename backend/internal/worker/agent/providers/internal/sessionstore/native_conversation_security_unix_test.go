//go:build !windows

package sessionstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeConversationRejectsWritableFilesDirectoriesAndLinks(t *testing.T) {
	q, path := nativeConversationFixture(t, 1)
	require.NoError(t, os.Chmod(path, 0o666))
	_, err := ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationUnsafe)
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o777))
	_, err = ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationUnsafe)
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o700))
	require.NoError(t, os.Rename(path, path+".original"))
	require.NoError(t, os.Symlink(path+".original", path))
	_, err = ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationUnsafe)
}

func TestNativeConversationRejectsFileReplacementDuringValidation(t *testing.T) {
	q, path := nativeConversationFixture(t, 1)
	original := q.ValidateRecord
	changed := false
	q.ValidateRecord = func(raw []byte) (bool, error) {
		if !changed {
			changed = true
			replacement := path + ".replacement"
			if err := os.WriteFile(replacement, []byte("{\"session\":\"test-session\",\"text\":\"replacement\"}\n"), 0o600); err != nil {
				return false, err
			}
			if err := os.Rename(replacement, path); err != nil {
				return false, err
			}
		}
		return original(raw)
	}
	page, err := ReadNativeConversationFile(context.Background(), q)
	require.ErrorIs(t, err, ErrNativeConversationUnsafe)
	require.Empty(t, page.Records)
}
