package service

import (
	"context"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/agentreport"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentReportsBindTheOriginalProcessEvenWhenPIDWasAlreadyReused(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	identities := nativeIdentityFixture{11: procutil.ProcessIdentity{PID: 11, StartTime: 200}}
	rows := []*leapmuxv1.NativeAgentProcess{{Pid: 11}}
	valid := agent.NativeSessionObservation{
		PID: 11, ProcessStartTime: 200, SessionID: "valid", DisplayName: "Reported chat",
		AgentReported: true, ReportedAt: now.Add(-time.Minute),
		ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
	}
	observations := []agent.NativeSessionObservation{valid}
	for _, tc := range []struct {
		id     string
		change func(*agent.NativeSessionObservation)
	}{
		{"previous-process", func(o *agent.NativeSessionObservation) { o.ProcessStartTime = 100 }},
		{"unbound", func(o *agent.NativeSessionObservation) { o.ProcessStartTime = 0 }},
		{"future", func(o *agent.NativeSessionObservation) { o.ReportedAt = now.Add(time.Nanosecond) }},
		{"missing-time", func(o *agent.NativeSessionObservation) { o.ReportedAt = time.Time{} }},
		{"expired", func(o *agent.NativeSessionObservation) { o.ReportedAt = now.Add(-agentreport.MaxAge - time.Nanosecond) }},
	} {
		observation := valid
		observation.SessionID = tc.id
		tc.change(&observation)
		observations = append(observations, observation)
	}
	applyNativeSessionsAt(context.Background(), identities, rows, identities, observations, now)
	require.Len(t, rows[0].Sessions, 1)
	session := rows[0].Sessions[0]
	assert.Equal(t, valid.SessionID, session.SessionId)
	assert.Equal(t, valid.DisplayName, session.DisplayName)
	assert.True(t, session.AgentReported)
	assert.Equal(t, valid.ActivityState, session.ActivityState)
	assert.Equal(t, timefmt.Format(valid.ReportedAt), session.ReportedAt)
	assert.Equal(t, timefmt.Format(valid.ReportedAt.Add(agentreport.FreshFor)), session.ReportExpiresAt)
}

func TestAgentReportsExpireWithoutPollingRenewingTheirTimestamp(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	identities := nativeIdentityFixture{11: procutil.ProcessIdentity{PID: 11, StartTime: 200}}
	observations := []agent.NativeSessionObservation{
		{PID: 11, ProcessStartTime: 200, SessionID: "one", DisplayName: "First chat",
			AgentReported: true, ReportedAt: now, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
		{PID: 11, ProcessStartTime: 200, SessionID: "two", DisplayName: "Second chat",
			AgentReported: true, ReportedAt: now, ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER},
		{PID: 11, SessionID: "provider-observed", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
	}
	for _, elapsed := range []time.Duration{0, agentreport.FreshFor, agentreport.MaxAge} {
		rows := []*leapmuxv1.NativeAgentProcess{{Pid: 11}}
		applyNativeSessionsAt(context.Background(), identities, rows, identities, observations, now.Add(elapsed))
		require.Len(t, rows[0].Sessions, 3)
		for i := range 2 {
			session := rows[0].Sessions[i]
			assert.True(t, session.AgentReported)
			assert.Equal(t, observations[i].DisplayName, session.DisplayName)
			assert.Equal(t, timefmt.Format(now), session.ReportedAt)
			assert.Equal(t, timefmt.Format(now.Add(agentreport.FreshFor)), session.ReportExpiresAt)
			expected := observations[i].ActivityState
			if elapsed >= agentreport.FreshFor {
				expected = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
			}
			assert.Equal(t, expected, session.ActivityState)
		}
		provider := rows[0].Sessions[2]
		assert.False(t, provider.AgentReported)
		assert.Empty(t, provider.ReportedAt)
		assert.Empty(t, provider.ReportExpiresAt)
		assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, provider.ActivityState)
	}
}
