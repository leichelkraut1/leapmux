package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type nativeIdentityFixture map[int]procutil.ProcessIdentity

func (s nativeIdentityFixture) ObservationIdentity(ctx context.Context, pid int) (procutil.ProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return procutil.ProcessIdentity{}, err
	}
	identity, ok := s[pid]
	if !ok {
		return identity, errors.New("process disappeared")
	}
	return identity, nil
}

func TestApplyNativeSessionsVerifiesIdentityAndExcludesUnmatchedProcesses(t *testing.T) {
	t.Parallel()
	before := nativeIdentityFixture{11: {PID: 11, StartTime: 100}, 12: {PID: 12, StartTime: 100}, 13: {PID: 13, StartTime: 100}}
	after := nativeIdentityFixture{11: before[11], 12: {PID: 12, StartTime: 200}}
	rows := []*leapmuxv1.NativeAgentProcess{{Pid: 11}, {Pid: 12}, {Pid: 13}}
	applyNativeSessions(context.Background(), after, rows, before, []agent.NativeSessionObservation{
		{PID: 11, SessionID: "session-one", DisplayName: "  Review\n dashboard\t ", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
		{PID: 11, SessionID: "session-two", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER},
		{PID: 12, SessionID: "reused-pid", DisplayName: "Reused process name", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		{PID: 13, SessionID: "exited-pid", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		{PID: 100, SessionID: "managed-or-other-user", DisplayName: "Excluded process name", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
	})
	require.Len(t, rows[0].Sessions, 2)
	assert.Equal(t, "session-one", rows[0].Sessions[0].SessionId)
	assert.Equal(t, "Review dashboard", rows[0].Sessions[0].DisplayName)
	assert.Empty(t, rows[0].Sessions[1].DisplayName)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, rows[0].Sessions[1].ActivityState)
	assert.Empty(t, rows[1].Sessions)
	assert.Empty(t, rows[2].Sessions)
}

func TestApplyNativeSessionsRejectsConflictsAndSanitizesState(t *testing.T) {
	t.Parallel()
	identities := nativeIdentityFixture{11: {PID: 11, StartTime: 100}}
	rows := []*leapmuxv1.NativeAgentProcess{{Pid: 11}}
	applyNativeSessions(context.Background(), identities, rows, identities, []agent.NativeSessionObservation{
		{PID: 11, SessionID: "conflict", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE},
		{PID: 11, SessionID: "conflict", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING},
		{PID: 11, SessionID: "/private/transcript"},
		{PID: 11, SessionID: ""},
		{PID: 11, SessionID: "unknown-state", ActivityState: leapmuxv1.AgentActivityState(999)},
	})
	require.Len(t, rows[0].Sessions, 1)
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED, rows[0].Sessions[0].ActivityState)
	data, err := protojson.Marshal(rows[0])
	require.NoError(t, err)
	assert.NotContains(t, string(data), "private")
	assert.NotContains(t, string(data), "999")
}

func TestObserveNativeSessionsFailuresLeaveProcessOnlyFallback(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	reg, ok := svc.Agents.Registry().Registration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.True(t, ok)
	reg.ObserveNativeSessions = func(context.Context) ([]agent.NativeSessionObservation, error) {
		return []agent.NativeSessionObservation{{PID: 11, SessionID: "discarded", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE}}, errors.New("unavailable")
	}
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	result := &leapmuxv1.InspectNativeAgentProcessesResponse{Processes: []*leapmuxv1.NativeAgentProcess{{Pid: 11, AgentProvider: reg.Provider}}}
	observeNativeSessions(context.Background(), nativeIdentityFixture{11: {PID: 11, StartTime: 100}}, result, nativeIdentityFixture{11: {PID: 11, StartTime: 100}}, registry)
	assert.Empty(t, result.Processes[0].Sessions)
	assert.Len(t, result.Processes, 1)
}

func TestObserveNativeSessionsSkipsUnrepresentedProviderAndCanceledScan(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	reg, ok := svc.Agents.Registry().Registration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.True(t, ok)
	called := false
	reg.ObserveNativeSessions = func(context.Context) ([]agent.NativeSessionObservation, error) { called = true; return nil, nil }
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	observeNativeSessions(context.Background(), nativeIdentityFixture{}, &leapmuxv1.InspectNativeAgentProcessesResponse{}, nil, registry)
	assert.False(t, called)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := &leapmuxv1.InspectNativeAgentProcessesResponse{Processes: []*leapmuxv1.NativeAgentProcess{{Pid: 11, AgentProvider: reg.Provider}}}
	observeNativeSessions(ctx, nativeIdentityFixture{11: {PID: 11, StartTime: 100}}, result, nativeIdentityFixture{11: {PID: 11, StartTime: 100}}, registry)
	assert.False(t, called)
	assert.Empty(t, result.Processes[0].Sessions)
}

func TestApplyNativeSessionsCapsRowsAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	identities := nativeIdentityFixture{11: {PID: 11, StartTime: 100}}
	observations := make([]agent.NativeSessionObservation, maxNativeSessions+1)
	for i := range observations {
		observations[i] = agent.NativeSessionObservation{PID: 11, SessionID: fmt.Sprintf("session-%d", i), ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE}
	}
	rows := []*leapmuxv1.NativeAgentProcess{{Pid: 11}}
	applyNativeSessions(context.Background(), identities, rows, identities, observations)
	assert.Len(t, rows[0].Sessions, maxNativeSessions)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows = []*leapmuxv1.NativeAgentProcess{{Pid: 11}}
	applyNativeSessions(ctx, identities, rows, identities, observations)
	assert.Empty(t, rows[0].Sessions)
}

func TestObserveNativeSessionsReturnsWhileCanceledProviderCleansUp(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	reg, ok := svc.Agents.Registry().Registration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.True(t, ok)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := make(chan struct{})
	defer close(cleanup)
	reg.ObserveNativeSessions = func(context.Context) ([]agent.NativeSessionObservation, error) {
		cancel()
		<-cleanup
		return []agent.NativeSessionObservation{{PID: 11, SessionID: "late", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE}}, nil
	}
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	identities := nativeIdentityFixture{11: {PID: 11, StartTime: 100}}
	result := &leapmuxv1.InspectNativeAgentProcessesResponse{Processes: []*leapmuxv1.NativeAgentProcess{{Pid: 11, AgentProvider: reg.Provider}}}
	observeNativeSessions(ctx, identities, result, identities, registry)
	assert.Empty(t, result.Processes[0].Sessions, "late results never mutate the response after cancellation")
}

func TestNormalizeNativeSessionDisplayName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"absent", "", ""},
		{"whitespace", " \t Review\n dashboard\r\n ", "Review dashboard"},
		{"controls and formatting", "Fix\x00\u202Estatus\u2069\u200B dashboard", "Fix status dashboard"},
		{"controls only", "\x00\u202E\u2069\t", ""},
		{"unicode", "修复状态 🧭", "修复状态 🧭"},
		{"literal markup", "<b>Review & fix</b>", "<b>Review & fix</b>"},
		{"limit", strings.Repeat("a", maxNativeSessionDisplayNameRunes), strings.Repeat("a", maxNativeSessionDisplayNameRunes)},
		{"over limit", strings.Repeat("a", maxNativeSessionDisplayNameRunes+1), strings.Repeat("a", maxNativeSessionDisplayNameRunes)},
		{"unicode limit", strings.Repeat("🧭", maxNativeSessionDisplayNameRunes+1), strings.Repeat("🧭", maxNativeSessionDisplayNameRunes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeNativeSessionDisplayName(tc.input))
		})
	}
}
