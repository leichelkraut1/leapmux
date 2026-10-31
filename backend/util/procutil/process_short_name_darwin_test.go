//go:build darwin

package procutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/unix"
)

func TestShortNameFromKinfoPreservesTruncatedNames(t *testing.T) {
	t.Parallel()
	var info unix.KinfoProc
	copy(info.Proc.P_comm[:], "long-process-na")
	assert.Equal(t, "long-process-na", shortNameFromKinfo(&info), "a long short-name is returned without argv expansion")
	clear(info.Proc.P_comm[:])
	copy(info.Proc.P_comm[:], "codex")
	assert.Equal(t, "codex", shortNameFromKinfo(&info))
}
