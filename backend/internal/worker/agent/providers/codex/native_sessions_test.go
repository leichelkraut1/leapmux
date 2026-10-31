package codex

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/agentreport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeSessionReportsPreserveClaimsAndExpiry(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir := filepath.Join(parent, "reports")
	now := time.Now().UTC()
	for _, state := range []string{"working", "waiting", "idle"} {
		require.NoError(t, agentreport.Write(dir, agentreport.Report{
			SchemaVersion: 1, Provider: "codex", SessionID: state, DisplayName: "Named task",
			PID: 42, ProcessStartTime: now.Add(-time.Hour).UnixMilli(), State: state, ReportedAt: now,
		}))
	}
	rows, err := observeNativeSessionsInDirectory(context.Background(), dir, now)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	states := map[string]leapmuxv1.AgentActivityState{}
	for _, row := range rows {
		assert.True(t, row.AgentReported)
		assert.True(t, now.Equal(row.ReportedAt))
		assert.Equal(t, now.Add(-time.Hour).UnixMilli(), row.ProcessStartTime)
		assert.Equal(t, "Named task", row.DisplayName)
		states[row.SessionID] = row.ActivityState
	}
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, states["working"])
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, states["waiting"])
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, states["idle"])
	rows, err = observeNativeSessionsInDirectory(context.Background(), dir, now.Add(agentreport.FreshFor))
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, row := range rows {
		assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, row.ActivityState)
		assert.True(t, now.Equal(row.ReportedAt))
	}
	rows, err = observeNativeSessionsInDirectory(context.Background(), dir, now.Add(agentreport.MaxAge+time.Second))
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestNativeSessionReportsMissingAndCanceled(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir := filepath.Join(parent, "missing")
	rows, err := observeNativeSessionsInDirectory(context.Background(), dir, time.Now())
	require.NoError(t, err)
	assert.Empty(t, rows)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = observeNativeSessionsInDirectory(ctx, dir, time.Now())
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotNil(t, Registration().ObserveNativeSessions)
}
