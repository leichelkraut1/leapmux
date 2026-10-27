package procutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessObservationRejectsMissingProcess(t *testing.T) {
	t.Parallel()
	for _, table := range []*ProcessTable{nil, NewProcessTable([]ProcessRecord{{PID: 123, PPID: 1}})} {
		user, err := table.UserKey(context.Background(), 123)
		require.Error(t, err)
		assert.Empty(t, user)
		path, err := table.Executable(context.Background(), 123)
		require.Error(t, err)
		assert.Empty(t, path)
		name, err := table.ShortName(context.Background(), 123)
		require.Error(t, err)
		assert.Empty(t, name)
	}
}
