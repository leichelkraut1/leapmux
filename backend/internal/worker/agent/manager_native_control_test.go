package agent_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeControlFixture struct {
	sends      atomic.Int32
	interrupts atomic.Int32
	closes     atomic.Int32
	stale      atomic.Bool
	send       func(context.Context, agent.NativeSessionInput) (agent.NativeSessionInputReceipt, error)
}

func (f *nativeControlFixture) Inspect(_ context.Context, sessionID string) (agent.NativeSessionControlState, error) {
	return agent.NativeSessionControlState{SessionID: sessionID, OwnerPresent: true}, nil
}
func (f *nativeControlFixture) Send(ctx context.Context, input agent.NativeSessionInput) (agent.NativeSessionInputReceipt, error) {
	f.sends.Add(1)
	if f.stale.Load() {
		return agent.NativeSessionInputReceipt{}, agent.ErrNativeControlStale
	}
	if f.send != nil {
		return f.send(ctx, input)
	}
	return agent.NativeSessionInputReceipt{Disposition: leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_QUEUED}, nil
}
func (f *nativeControlFixture) Interrupt(context.Context, agent.NativeSessionInterrupt) error {
	f.interrupts.Add(1)
	return nil
}
func (f *nativeControlFixture) Close() error { f.closes.Add(1); return nil }

func nativeControlManager(t *testing.T, fixture *nativeControlFixture) *agent.Manager {
	t.Helper()
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	reg.NewNativeSessionController = func(agent.NativeSessionControllerOptions) agent.NativeSessionController { return fixture }
	registry, err := agent.NewRegistry(reg)
	require.NoError(t, err)
	manager := agent.NewManager(registry, nil)
	t.Cleanup(manager.StopAll)
	return manager
}

func TestNativeControlDeduplicatesConcurrentInputsAndRejectsChangedPayloads(t *testing.T) {
	fixture := &nativeControlFixture{}
	manager := nativeControlManager(t, fixture)
	input := agent.NativeSessionInput{SessionID: "generated", BindingToken: "fixture-binding", RequestID: "generated-request", Text: "harmless generated marker"}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			receipt, err := manager.SendNativeSessionInput(context.Background(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, input)
			assert.NoError(t, err)
			assert.Equal(t, leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_QUEUED, receipt.Disposition)
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), fixture.sends.Load())
	for _, changed := range []agent.NativeSessionInput{
		{SessionID: "other", BindingToken: input.BindingToken, RequestID: input.RequestID, Text: input.Text},
		{SessionID: input.SessionID, BindingToken: "new-owner", RequestID: input.RequestID, Text: input.Text},
		{SessionID: input.SessionID, BindingToken: input.BindingToken, RequestID: input.RequestID, Text: "different message"},
	} {
		_, err := manager.SendNativeSessionInput(context.Background(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, changed)
		require.ErrorIs(t, err, agent.ErrNativeControlInvalid)
	}
	err := manager.InterruptNativeSession(context.Background(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, agent.NativeSessionInterrupt{SessionID: input.SessionID, BindingToken: input.BindingToken, RequestID: input.RequestID, ExpectedTurnID: "generated-turn"})
	require.ErrorIs(t, err, agent.ErrNativeControlInvalid)
	assert.Zero(t, fixture.interrupts.Load())
	assert.Equal(t, int32(1), fixture.sends.Load())
}

func TestNativeControlRemembersUncertainDeliveryAndCompletedRequestsBeforeLeaseValidation(t *testing.T) {
	fixture := &nativeControlFixture{send: func(context.Context, agent.NativeSessionInput) (agent.NativeSessionInputReceipt, error) {
		return agent.NativeSessionInputReceipt{}, agent.ErrNativeControlUncertain
	}}
	manager := nativeControlManager(t, fixture)
	input := agent.NativeSessionInput{SessionID: "generated", BindingToken: "old-owner", RequestID: "uncertain-request", Text: "harmless marker"}
	_, err := manager.SendNativeSessionInput(context.Background(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, input)
	require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
	fixture.stale.Store(true)
	_, err = manager.SendNativeSessionInput(context.Background(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, input)
	require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
	assert.Equal(t, int32(1), fixture.sends.Load())
}

func TestNativeControlCanceledDuplicateDoesNotReplayOrCancelOriginal(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fixture := &nativeControlFixture{send: func(context.Context, agent.NativeSessionInput) (agent.NativeSessionInputReceipt, error) {
		close(entered)
		<-release
		return agent.NativeSessionInputReceipt{Disposition: leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_ACCEPTED}, nil
	}}
	manager := nativeControlManager(t, fixture)
	input := agent.NativeSessionInput{SessionID: "generated", BindingToken: "fixture-owner", RequestID: "pending-request", Text: "harmless marker"}
	finished := make(chan error, 1)
	go func() {
		_, err := manager.SendNativeSessionInput(context.Background(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, input)
		finished <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := manager.SendNativeSessionInput(ctx, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, input)
	require.ErrorIs(t, err, context.Canceled)
	close(release)
	require.NoError(t, <-finished)
	assert.Equal(t, int32(1), fixture.sends.Load())
	manager.StopAll()
	manager.StopAll()
	assert.Equal(t, int32(1), fixture.closes.Load())
	_, err = manager.SendNativeSessionInput(context.Background(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, input)
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
}

func TestNativeControlLeaseRequiresExactBindingAndFreshOwnerEvidence(t *testing.T) {
	now := time.Now()
	state := agent.NativeSessionControlState{BindingToken: "fixture-owner", ObservedAt: now, ExpiresAt: now.Add(30 * time.Second)}
	assert.True(t, agent.ValidNativeControlLease(state, "fixture-owner", now))
	assert.False(t, agent.ValidNativeControlLease(state, "other-owner", now))
	assert.False(t, agent.ValidNativeControlLease(state, "fixture-owner", state.ExpiresAt))
	state.ObservedAt = now.Add(time.Minute)
	assert.False(t, agent.ValidNativeControlLease(state, "fixture-owner", now))
}

type firstNativeControlContextCheck struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *firstNativeControlContextCheck) Err() error {
	// Capture before signaling, so cancellation after the signal cannot turn
	// the initial successful check into a cancellation before the lock wait.
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestNativeControlDoesNotConstructOrSendAfterCancellationWhileWaitingForFactory(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	first, second := &nativeControlFixture{}, &nativeControlFixture{}
	firstReg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	firstReg.NewNativeSessionController = func(agent.NativeSessionControllerOptions) agent.NativeSessionController {
		close(entered)
		<-release
		return first
	}
	var secondFactories atomic.Int32
	secondReg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	secondReg.NewNativeSessionController = func(agent.NativeSessionControllerOptions) agent.NativeSessionController {
		secondFactories.Add(1)
		return second
	}
	registry, err := agent.NewRegistry(firstReg, secondReg)
	require.NoError(t, err)
	manager := agent.NewManager(registry, nil)
	t.Cleanup(manager.StopAll)
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.InspectNativeSessionControl(context.Background(), firstReg.Provider, "first-session")
		firstDone <- err
	}()
	<-entered
	base, cancel := context.WithCancel(context.Background())
	ctx := &firstNativeControlContextCheck{Context: base, checked: make(chan struct{})}
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.SendNativeSessionInput(ctx, secondReg.Provider, agent.NativeSessionInput{SessionID: "second-session", RequestID: "generated-request"})
		secondDone <- err
	}()
	<-ctx.checked
	cancel()
	close(release)
	require.NoError(t, <-firstDone)
	require.ErrorIs(t, <-secondDone, context.Canceled)
	assert.Zero(t, secondFactories.Load())
	assert.Zero(t, second.sends.Load())
}
