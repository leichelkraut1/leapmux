//go:build !unix && !windows

package agentreport

import "io/fs"

func mkdirPrivate(string) error { return ErrUnavailable }
func restrictDirectory(string, fs.FileInfo) error { return ErrUnavailable }
func checkPrivate(string, fs.FileInfo) error { return ErrUnavailable }
