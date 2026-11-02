package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/require"
)

const controlTestSession = "generated-session"
const controlTestInstance = "generated-instance"

func controlTestController(t *testing.T) (*nativeControlController, *quartz.Mock) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	controller := newClaudeNativeController(agent.NativeSessionControllerOptions{
		Directory: filepath.Join(t.TempDir(), "native-control", "claude"),
	}, clock, func(ctx context.Context, id string) (nativeControlOwner, error) {
		if err := ctx.Err(); err != nil {
			return nativeControlOwner{}, err
		}
		if id != controlTestSession {
			return nativeControlOwner{}, agent.ErrNativeControlUnavailable
		}
		return nativeControlOwner{identity: procutil.ProcessIdentity{PID: 43210, StartTime: 12345},
			activity: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE}, nil
	})
	require.NoError(t, controller.initErr)
	t.Cleanup(func() { require.NoError(t, controller.Close()) })
	return controller, clock
}

func controlTestFrame() nativeControlFrame {
	return nativeControlFrame{SessionID: controlTestSession, InstanceID: controlTestInstance, Version: "2.1.308"}
}

func controlTestPost(t *testing.T, c *nativeControlController, path string, frame nativeControlFrame, origin, token string) (int, map[string]json.RawMessage) {
	t.Helper()
	data, err := json.Marshal(frame)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+c.listener.Addr().String()+path, bytes.NewReader(data))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	var body map[string]json.RawMessage
	if response.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	}
	return response.StatusCode, body
}

func controlTestRegister(t *testing.T, c *nativeControlController) agent.NativeSessionControlState {
	t.Helper()
	status, _ := controlTestPost(t, c, "/register", controlTestFrame(), "", c.token)
	require.Equal(t, http.StatusOK, status)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.True(t, state.CanSend)
	require.Equal(t, leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_QUEUE_NEXT_TURN, state.InputBehavior)
	return state
}

func controlTestInput(state agent.NativeSessionControlState, requestID string) agent.NativeSessionInput {
	return agent.NativeSessionInput{SessionID: controlTestSession, BindingToken: state.BindingToken,
		RequestID: requestID, Text: "generated harmless marker",
		Behavior: leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_QUEUE_NEXT_TURN}
}

func controlTestOutcome(t *testing.T, c *nativeControlController, requestID, state, turnID string) nativeControlOutcome {
	t.Helper()
	c.mu.Lock()
	command := c.peers[controlTestSession].byRequest[requestID]
	c.mu.Unlock()
	require.NotNil(t, command)
	return nativeControlOutcome{RequestID: requestID, DeliveryID: command.DeliveryID, State: state, TurnID: turnID}
}

func TestNativeControlAuthenticationAndEnrollment(t *testing.T) {
	c, _ := controlTestController(t)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.False(t, state.CanSend)
	require.True(t, state.OwnerPresent)
	status, _ := controlTestPost(t, c, "/register", controlTestFrame(), "", "incorrect-token")
	require.Equal(t, http.StatusForbidden, status)
	status, _ = controlTestPost(t, c, "/register", controlTestFrame(), "https://untrusted.invalid", c.token)
	require.Equal(t, http.StatusForbidden, status)
	frame := controlTestFrame()
	frame.SessionID = "different-generated-session"
	status, _ = controlTestPost(t, c, "/register", frame, "", c.token)
	require.Equal(t, http.StatusConflict, status)
	controlTestRegister(t, c)
	require.NoError(t, nativeControlCheckPrivate(filepath.Join(c.directory, "bridge.json")))
	require.NoError(t, nativeControlCheckPrivate(filepath.Join(c.directory, "plugin", "hooks", "register.js")))
	plugin, err := os.ReadFile(filepath.Join(c.directory, "plugin", "hooks", "register.js"))
	require.NoError(t, err)
	require.NotContains(t, string(plugin), c.token)
	require.NotContains(t, string(plugin), "__LEAPMUX_")
	require.Contains(t, string(plugin), "LEAPMUX_WORKER")
}

func TestNativeControlDispatchOnceAndOutcome(t *testing.T) {
	c, _ := controlTestController(t)
	state := controlTestRegister(t, c)
	input := controlTestInput(state, "generated-request")
	for range 2 {
		receipt, err := c.Send(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, leapmuxv1.NativeSessionInputDisposition_NATIVE_SESSION_INPUT_DISPOSITION_QUEUED, receipt.Disposition)
	}
	status, body := controlTestPost(t, c, "/poll", controlTestFrame(), "", c.token)
	require.Equal(t, http.StatusOK, status)
	var commands []nativeControlCommand
	require.NoError(t, json.Unmarshal(body["commands"], &commands))
	require.Len(t, commands, 1)
	_, body = controlTestPost(t, c, "/poll", controlTestFrame(), "", c.token)
	require.NoError(t, json.Unmarshal(body["commands"], &commands))
	require.Empty(t, commands, "reconnect must never replay a dispatched prompt")
	frame := controlTestFrame()
	frame.Outcomes = []nativeControlOutcome{controlTestOutcome(t, c, input.RequestID, "started", "generated-turn")}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.Len(t, state.RecentInputs, 1)
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_STARTED, state.RecentInputs[0].State)
	input.Text = "changed payload"
	_, err = c.Send(t.Context(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlInvalid)
	input = controlTestInput(state, "wrong-behavior")
	input.Behavior = leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_SEND_NOW
	_, err = c.Send(t.Context(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlInvalid)
}

func TestNativeControlLeaseExpiresAndReloadInvalidates(t *testing.T) {
	c, clock := controlTestController(t)
	old := controlTestRegister(t, c)
	clock.Advance(time.Duration(contracts.NativeSessionControlLeaseSeconds+1) * time.Second).MustWait(testutil.DeadlineContext(t))
	_, err := c.Send(t.Context(), controlTestInput(old, "expired-input"))
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
	controlTestPost(t, c, "/poll", controlTestFrame(), "", c.token)
	fresh, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	_, err = c.Send(t.Context(), controlTestInput(fresh, "before-reload"))
	require.NoError(t, err)
	frame := controlTestFrame()
	frame.InstanceID = "replacement-generated-instance"
	status, _ := controlTestPost(t, c, "/register", frame, "", c.token)
	require.Equal(t, http.StatusOK, status)
	_, err = c.Send(t.Context(), controlTestInput(fresh, "stale-owner-input"))
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED, state.RecentInputs[0].State)
}

func TestNativeControlLocalStopAndExactInterrupt(t *testing.T) {
	c, _ := controlTestController(t)
	controlTestRegister(t, c)
	frame := controlTestFrame()
	frame.ActiveTurn = "exact-generated-turn"
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	result := make(chan error, 1)
	started := make(chan struct{})
	// The test waits on the command's enqueue rather than a timing window.
	go func() {
		command, enqueueErr := c.enqueue(t.Context(), controlTestSession, state.BindingToken, &nativeControlCommand{
			RequestID: "exact-interrupt", Kind: "interrupt", TurnID: "exact-generated-turn"})
		close(started)
		if enqueueErr != nil {
			result <- enqueueErr
			return
		}
		select {
		case <-command.done:
			result <- nil
		case <-t.Context().Done():
			result <- t.Context().Err()
		}
	}()
	<-started
	_, body := controlTestPost(t, c, "/poll", frame, "", c.token)
	var commands []nativeControlCommand
	require.NoError(t, json.Unmarshal(body["commands"], &commands))
	require.Len(t, commands, 1)
	frame.ActiveTurn = ""
	frame.Outcomes = []nativeControlOutcome{controlTestOutcome(t, c, "exact-interrupt", "interrupted", "exact-generated-turn")}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	require.NoError(t, <-result)
	frame.Stopped = true
	frame.Outcomes = nil
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.False(t, state.CanSend)
	require.False(t, state.CanInterrupt)
	_, err = c.Send(t.Context(), controlTestInput(state, "blocked-after-stop"))
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
}

func TestNativeControlConcurrentDuplicateAndExclusiveDirectory(t *testing.T) {
	c, _ := controlTestController(t)
	state := controlTestRegister(t, c)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			_, err := c.Send(t.Context(), controlTestInput(state, "same-concurrent-request"))
			require.NoError(t, err)
		})
	}
	group.Wait()
	c.mu.Lock()
	require.Len(t, c.peers[controlTestSession].commands, 1)
	c.mu.Unlock()
	other := newClaudeNativeController(agent.NativeSessionControllerOptions{Directory: c.directory}, quartz.NewReal(), c.verifyOwner)
	require.Error(t, other.initErr)
	require.NoError(t, other.Close())
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.True(t, state.CanSend, "a second controller must not replace the first descriptor or listener")
}

func TestNativeControlRequiresPrivateDirectoryAndVersions(t *testing.T) {
	c := newNativeSessionController(agent.NativeSessionControllerOptions{})
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.Inspect(t.Context(), controlTestSession)
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	for _, version := range []string{"2.1.308", "2.1.287", "2.2.0"} {
		require.True(t, nativeControlVersionAllowed(version))
	}
	for _, version := range []string{"2.1.286", "2.1.308-extra", "", "bad"} {
		require.False(t, nativeControlVersionAllowed(version))
	}
}

func TestNativeControlUncertainDeliveryRemainsQueryable(t *testing.T) {
	c, _ := controlTestController(t)
	state := controlTestRegister(t, c)
	_, err := c.Send(t.Context(), controlTestInput(state, "unconfirmed-generated-input"))
	require.NoError(t, err)
	controlTestPost(t, c, "/poll", controlTestFrame(), "", c.token)
	c.verifyOwner = func(context.Context, string) (nativeControlOwner, error) {
		return nativeControlOwner{}, agent.ErrNativeControlUnavailable
	}
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	require.False(t, state.CanSend)
	require.Empty(t, state.BindingToken)
	require.Len(t, state.RecentInputs, 1)
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN, state.RecentInputs[0].State)
}

func TestNativeControlRequiresExactInterruptAcknowledgement(t *testing.T) {
	c, _ := controlTestController(t)
	controlTestRegister(t, c)
	frame := controlTestFrame()
	frame.ActiveTurn = "exact-generated-turn"
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	command, err := c.enqueue(t.Context(), controlTestSession, state.BindingToken, &nativeControlCommand{
		RequestID: "negative-interrupt", Kind: "interrupt", TurnID: "exact-generated-turn"})
	require.NoError(t, err)
	controlTestPost(t, c, "/poll", frame, "", c.token)
	frame.Outcomes = []nativeControlOutcome{controlTestOutcome(t, c, "negative-interrupt", "started", "exact-generated-turn")}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	c.mu.Lock()
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED, command.state)
	c.mu.Unlock()
	frame.Outcomes = []nativeControlOutcome{controlTestOutcome(t, c, "negative-interrupt", "interrupted", "different-generated-turn")}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	c.mu.Lock()
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED, command.state)
	c.mu.Unlock()
	frame.ActiveTurn, frame.Outcomes = "", nil
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	input := controlTestInput(state, "send-cannot-be-interrupt")
	_, err = c.Send(t.Context(), input)
	require.NoError(t, err)
	controlTestPost(t, c, "/poll", frame, "", c.token)
	frame.Outcomes = []nativeControlOutcome{controlTestOutcome(t, c, input.RequestID, "interrupted", "exact-generated-turn")}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_REJECTED, state.RecentInputs[len(state.RecentInputs)-1].State)
	c.mu.Lock()
	require.Empty(t, c.peers[controlTestSession].byRequest[input.RequestID].Text)
	c.mu.Unlock()
}

func TestNativeControlRetiresOnlyExpiredConfirmedRecords(t *testing.T) {
	c, clock := controlTestController(t)
	state := controlTestRegister(t, c)
	first := controlTestInput(state, "old-confirmed")
	_, err := c.Send(t.Context(), first)
	require.NoError(t, err)
	_, err = c.Send(t.Context(), controlTestInput(state, "old-uncertain"))
	require.NoError(t, err)
	controlTestPost(t, c, "/poll", controlTestFrame(), "", c.token)
	frame := controlTestFrame()
	oldOutcome := controlTestOutcome(t, c, first.RequestID, "started", "old-generated-turn")
	frame.Outcomes = []nativeControlOutcome{oldOutcome}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	verify := c.verifyOwner
	c.verifyOwner = func(context.Context, string) (nativeControlOwner, error) {
		return nativeControlOwner{}, agent.ErrNativeControlUnavailable
	}
	_, err = c.Inspect(t.Context(), controlTestSession)
	require.ErrorIs(t, err, agent.ErrNativeControlUnavailable)
	c.verifyOwner = verify
	frame.Outcomes = nil
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	_, err = c.Send(t.Context(), controlTestInput(state, "old-queued"))
	require.NoError(t, err)
	clock.Advance(time.Duration(2*contracts.NativeSessionControlLeaseSeconds+1) * time.Second).MustWait(testutil.DeadlineContext(t))
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	c.mu.Lock()
	peer := c.peers[controlTestSession]
	require.NotContains(t, peer.byRequest, "old-confirmed")
	require.Contains(t, peer.byRequest, "old-uncertain")
	require.Contains(t, peer.byRequest, "old-queued")
	require.Equal(t, 2, c.commandCount)
	c.mu.Unlock()
	require.Len(t, state.RecentInputs, 3, "recent outcomes remain visible after dedup retirement")
	_, err = c.Send(t.Context(), first)
	require.ErrorIs(t, err, agent.ErrNativeControlStale, "retired records never make an expired original binding valid")
	controlTestPost(t, c, "/poll", controlTestFrame(), "", c.token)
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	first.BindingToken = state.BindingToken
	_, err = c.Send(t.Context(), first)
	require.NoError(t, err, "an explicit new intent may reuse a retired ID with a fresh lease")
	frame.Outcomes = nil
	controlTestPost(t, c, "/poll", frame, "", c.token)
	currentOutcome := controlTestOutcome(t, c, first.RequestID, "started", "new-generated-turn")
	require.NotEqual(t, oldOutcome.DeliveryID, currentOutcome.DeliveryID)
	frame.Outcomes = []nativeControlOutcome{oldOutcome}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	c.mu.Lock()
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED, c.peers[controlTestSession].byRequest[first.RequestID].state,
		"a delayed old delivery acknowledgement cannot settle the new same-ID input")
	c.mu.Unlock()
	frame.Outcomes = []nativeControlOutcome{currentOutcome}
	controlTestPost(t, c, "/poll", frame, "", c.token)
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	seen := make(map[string]bool)
	for _, outcome := range state.RecentInputs {
		require.False(t, seen[outcome.RequestID], "shared response validation requires unique recent request IDs")
		seen[outcome.RequestID] = true
	}
	require.Len(t, state.RecentInputs, 3)
}

func TestNativeControlBoundsOutcomesAndRejectsForgedLease(t *testing.T) {
	c, _ := controlTestController(t)
	state := controlTestRegister(t, c)
	for index := range 21 {
		_, err := c.Send(t.Context(), controlTestInput(state, "generated-input-"+strconv.Itoa(index)))
		require.NoError(t, err)
	}
	fresh, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.Len(t, fresh.RecentInputs, 20)
	_, body := controlTestPost(t, c, "/poll", controlTestFrame(), "", c.token)
	var commands []nativeControlCommand
	require.NoError(t, json.Unmarshal(body["commands"], &commands))
	require.Len(t, commands, 8, "bounded batches must fit the native Mods HTTP response limit")
	input := controlTestInput(state, "forged-input")
	input.BindingToken = "A" + input.BindingToken[1:]
	if input.BindingToken == state.BindingToken {
		input.BindingToken = "B" + input.BindingToken[1:]
	}
	_, err = c.Send(t.Context(), input)
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
}
