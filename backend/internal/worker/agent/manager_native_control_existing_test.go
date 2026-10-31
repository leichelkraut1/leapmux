package agent_test

import (
	"context"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeControlExistingInspectionNeverCreatesAController(t *testing.T) {
	fixture := &nativeControlFixture{}
	var factories atomic.Int32
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reg.NewNativeSessionController = func(agent.NativeSessionControllerOptions) agent.NativeSessionController {
		factories.Add(1)
		return fixture
	}
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	manager := agent.NewManager(registry, nil)
	t.Cleanup(manager.StopAll)
	_, exists, err := manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "generated")
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	assert.False(t, exists)
	assert.Zero(t, factories.Load())
	_, err = manager.InspectNativeSessionControl(t.Context(), reg.Provider, "generated")
	require.NoError(t, err)
	state, exists, err := manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "generated")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "generated", state.SessionID)
	assert.Equal(t, int32(1), factories.Load())
	_, exists, err = manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "never-enrolled-history")
	require.NoError(t, err)
	assert.False(t, exists, "another session's bridge does not block historical resume")
	manager.StopAll()
	_, exists, err = manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "generated")
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	assert.True(t, exists, "a closed manager cannot prove that an enrolled owner is absent")
}

func TestNativeControlExistingInspectionHonorsCancelledContext(t *testing.T) {
	manager := nativeControlManager(t, &nativeControlFixture{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := manager.InspectExistingNativeSessionControl(ctx, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "generated")
	require.ErrorIs(t, err, context.Canceled)
}

type nativeEnrolledBeforeInspectionFixture struct {
	nativeControlFixture
	inspections atomic.Int32
	probeError  error
}

func (f *nativeEnrolledBeforeInspectionFixture) HasNativeSessionEnrollment(sessionID string) bool {
	return sessionID == "plugin-enrolled-before-viewer"
}

func (f *nativeEnrolledBeforeInspectionFixture) Inspect(_ context.Context, sessionID string) (agent.NativeSessionControlState, error) {
	f.inspections.Add(1)
	return agent.NativeSessionControlState{SessionID: sessionID, OwnerPresent: true}, f.probeError
}

func TestNativeControlExistingInspectionFindsEnrollmentBeforeFirstViewer(t *testing.T) {
	fixture := &nativeEnrolledBeforeInspectionFixture{}
	var factories atomic.Int32
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	reg.NewNativeSessionController = func(agent.NativeSessionControllerOptions) agent.NativeSessionController {
		factories.Add(1)
		return fixture
	}
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	manager := agent.NewManager(registry, nil)
	t.Cleanup(manager.StopAll)
	_, exists, err := manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "plugin-enrolled-before-viewer")
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	assert.False(t, exists)
	assert.Zero(t, factories.Load(), "resume cannot construct a controller")

	// Another viewer created the bridge; the plugin enrolled a second chat
	// directly, without ever calling Manager.Inspect for that session.
	_, err = manager.InspectNativeSessionControl(t.Context(), reg.Provider, "other-generated-viewer")
	require.NoError(t, err)
	assert.Equal(t, int32(1), fixture.inspections.Load())
	state, exists, err := manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "plugin-enrolled-before-viewer")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, state.OwnerPresent, "enrolled native owner must prevent a duplicate managed resume")
	assert.Equal(t, int32(2), fixture.inspections.Load())
	assert.Equal(t, int32(1), factories.Load())

	_, exists, err = manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "unrelated-historical-session")
	require.NoError(t, err)
	assert.False(t, exists)
	assert.Equal(t, int32(2), fixture.inspections.Load(), "unknown history is not probed or enrolled")
	fixture.probeError = agent.ErrNativeControlUnavailable
	_, exists, err = manager.InspectExistingNativeSessionControl(t.Context(), reg.Provider, "plugin-enrolled-before-viewer")
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	assert.True(t, exists, "loss of the enrolled owner's connection cannot establish safe absence")
}
