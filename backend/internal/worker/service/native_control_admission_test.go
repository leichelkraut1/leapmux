package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func TestNativeControlAdmissionCancelsWaitersAndReclaimsEntries(t *testing.T) {
	var admission nativeControlAdmission
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	release, err := admission.acquire(t.Context(), provider, "generated-session")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	entered, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		close(entered)
		unblock, err := admission.acquire(ctx, provider, "generated-session")
		if unblock != nil {
			unblock()
		}
		finished <- err
	}()
	<-entered
	cancel()
	require.ErrorIs(t, <-finished, context.Canceled)
	release()
	release()
	admission.mu.Lock()
	assert.Empty(t, admission.entries)
	admission.mu.Unlock()
	remaining, err := admission.acquire(t.Context(), provider, "generated-session")
	require.NoError(t, err)
	remaining()
}

func TestNativeControlAdmissionDoesNotBlockOtherNativeIdentities(t *testing.T) {
	var admission nativeControlAdmission
	first, err := admission.acquire(t.Context(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "generated-session")
	require.NoError(t, err)
	defer first()
	otherSession, err := admission.acquire(t.Context(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "another-session")
	require.NoError(t, err)
	defer otherSession()
	otherProvider, err := admission.acquire(t.Context(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "generated-session")
	require.NoError(t, err)
	defer otherProvider()
}

type nativeAdmissionController struct {
	state agent.NativeSessionControlState
	err   error
}

func (f *nativeAdmissionController) Inspect(context.Context, string) (agent.NativeSessionControlState, error) {
	return f.state, f.err
}
func (*nativeAdmissionController) Send(context.Context, agent.NativeSessionInput) (agent.NativeSessionInputReceipt, error) {
	return agent.NativeSessionInputReceipt{}, agent.ErrNativeControlUnavailable
}
func (*nativeAdmissionController) Interrupt(context.Context, agent.NativeSessionInterrupt) error {
	return agent.ErrNativeControlUnavailable
}
func (*nativeAdmissionController) Close() error { return nil }

func installNativeAdmissionFixture(t *testing.T, svc *Service, fixture *nativeAdmissionController) {
	t.Helper()
	reg, ok := svc.Agents.Registry().Registration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	require.True(t, ok)
	reg.NewNativeSessionController = func(agent.NativeSessionControllerOptions) agent.NativeSessionController { return fixture }
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	svc.Agents.StopAll()
	svc.Agents = agent.NewManager(registry, nil)
	t.Cleanup(svc.Agents.StopAll)
	original := fixture.state
	fixture.state.OwnerPresent = true
	_, _ = svc.Agents.InspectNativeSessionControl(t.Context(), reg.Provider, fixture.state.SessionID)
	fixture.state = original
}

func TestNativeResumeRefusesAnEnrolledOwnerAndUnresolvedInputsBeforeCreatingARow(t *testing.T) {
	for _, name := range []string{"live owner", "stopped owner", "disconnected owner", "queued", "dispatched", "uncertain", "probe failed"} {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			state := agent.NativeSessionControlState{SessionID: "generated-session"}
			fixture := &nativeAdmissionController{state: state}
			switch name {
			case "live owner", "stopped owner":
				fixture.state.BindingToken = "generated-live-owner-binding"
				fixture.state.ObservedAt = time.Now()
				fixture.state.ExpiresAt = time.Now().Add(30 * time.Second)
				fixture.state.CanSend = name == "live owner"
			case "probe failed":
				fixture.err = agent.ErrNativeControlUnavailable
			case "disconnected owner":
				fixture.state.OwnerPresent = true
				fixture.state.RecentInputs = []agent.NativeSessionInputOutcome{{RequestID: "generated-started", State: leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED}}
			default:
				states := map[string]leapmuxv1.NativeSessionInputState{
					"queued":     leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_QUEUED,
					"dispatched": leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED,
					"uncertain":  leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN,
				}
				fixture.state.RecentInputs = []agent.NativeSessionInputOutcome{{RequestID: "generated-request", State: states[name]}}
			}
			installNativeAdmissionFixture(t, svc, fixture)
			err := svc.createAgentRecordWithTranscript(t.Context(), db.CreateAgentParams{
				ID: "generated-target", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				WorkingDir: t.TempDir(), HomeDir: svc.HomeDir,
			}, state.SessionID)
			require.ErrorIs(t, err, errNativeSessionExternallyControlled)
			_, err = svc.Queries.GetAgentByID(t.Context(), "generated-target")
			require.ErrorIs(t, err, sql.ErrNoRows)
		})
	}
}

func TestNativeResumeAllowsHistoryWithoutAnEnrolledOwnerOrPendingInput(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "no existing bridge", true: "settled bridge history"}[enrolled], func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			if enrolled {
				installNativeAdmissionFixture(t, svc, &nativeAdmissionController{state: agent.NativeSessionControlState{
					SessionID: "generated-session", RecentInputs: []agent.NativeSessionInputOutcome{
						{RequestID: "generated-dropped", State: leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED},
						{RequestID: "generated-started", State: leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED},
					},
				}})
			}
			err := svc.createAgentRecordWithTranscript(t.Context(), db.CreateAgentParams{
				ID: "generated-target", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				WorkingDir: t.TempDir(), HomeDir: svc.HomeDir,
			}, "generated-session")
			require.NoError(t, err)
			row, err := svc.Queries.GetAgentByID(t.Context(), "generated-target")
			require.NoError(t, err)
			assert.Equal(t, "generated-session", row.PendingResumeSessionID)
		})
	}
}

func TestNativeControlRejectsDurablePendingClaimsBeforeAWorkerProcessExists(t *testing.T) {
	svc, _, _ := setupTestService(t)
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: "generated-pending-startup", AgentProvider: provider, WorkingDir: t.TempDir(), HomeDir: svc.HomeDir,
		PendingResumeSessionID: "generated-session",
	}))
	assert.Empty(t, svc.Agents.ListAgentIDs())
	external, err := svc.nativeControlIsExternal(t.Context(), provider, "generated-session")
	require.NoError(t, err)
	assert.False(t, external)
	external, err = svc.nativeControlIsExternal(t.Context(), provider, "another-session")
	require.NoError(t, err)
	assert.True(t, external)
}

func TestOpenAgentExplainsAnExternalOwnerConflictWithoutStartingADuplicate(t *testing.T) {
	svc, dispatcher, writer := setupTestService(t)
	installNativeAdmissionFixture(t, svc, &nativeAdmissionController{state: agent.NativeSessionControlState{
		SessionID: "generated-session", BindingToken: "generated-enrolled-owner",
	}})
	dispatch(dispatcher, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, AgentSessionId: "generated-session", WorkingDir: t.TempDir(),
	}, writer)
	require.Len(t, writer.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), writer.errors[0].code)
	assert.Contains(t, writer.errors[0].message, "original app")
	assert.Empty(t, writer.responses)
	assert.Empty(t, svc.Agents.ListAgentIDs())
}
