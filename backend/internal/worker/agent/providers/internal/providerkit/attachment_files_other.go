//go:build !unix && !windows

package providerkit

import "io/fs"

// The store requires a supported private-directory implementation.
func mkdirPrivateAttachmentDir(string) error {
	return errAttachmentStorageUnsafe
}

func checkPrivateAttachmentPath(string, fs.FileInfo) error {
	return errAttachmentStorageUnsafe
}
