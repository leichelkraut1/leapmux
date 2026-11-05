package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func controlTestApprovalFrame() nativeControlFrame {
	frame := controlTestFrame()
	frame.ActiveTurn = "generated-approval-turn"
	frame.ApprovalIDs = []string{"generated-permission-invocation"}
	frame.Approval = &nativeControlApprovalFrame{Protocol: 1, ID: frame.ApprovalIDs[0],
		TurnID: frame.ActiveTurn, Title: "Tool approval: Bash",
		Detail: `{"tool":"Bash","input":{"command":"printf harmless_generated_marker"},"cwd":"generated-fixture"}`}
	return frame
}

// The timer trap proves the actual loopback request has entered its wait after
// registering the prompt. Tests never guess at readiness using a sleep.
func controlTestHoldApproval(t *testing.T, c *nativeControlController, clock *quartz.Mock, frame nativeControlFrame) <-chan map[string]json.RawMessage {
	t.Helper()
	poll := frame
	poll.Approval = nil
	status, _ := controlTestPost(t, c, "/poll", poll, "", c.token)
	require.Equal(t, http.StatusOK, status)
	trap := clock.Trap().NewTimer("claude-native-approval-wait")
	defer trap.Close()
	result := make(chan map[string]json.RawMessage, 1)
	go func() {
		status, body := controlTestPost(t, c, "/approval", frame, "", c.token)
		require.Equal(t, http.StatusOK, status)
		result <- body
	}()
	call := trap.MustWait(testutil.DeadlineContext(t))
	require.NoError(t, call.Release(t.Context()))
	return result
}

func TestNativeControlApprovalExactAnswerAndNoReplay(t *testing.T) {
	for _, option := range []string{"allow_once", "deny", "original_app"} {
		t.Run(option, func(t *testing.T) {
			c, clock := controlTestController(t)
			controlTestRegister(t, c)
			frame := controlTestApprovalFrame()
			result := controlTestHoldApproval(t, c, clock, frame)
			state, err := c.Inspect(t.Context(), controlTestSession)
			require.NoError(t, err)
			require.Len(t, state.PendingApprovals, 1)
			prompt := state.PendingApprovals[0]
			require.True(t, prompt.CanAnswer)
			require.Equal(t, frame.Approval.Detail, prompt.Detail)
			require.NotEqual(t, frame.Approval.ID, prompt.PromptID)
			require.Len(t, prompt.Options, 3)
			require.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER, state.ActivityState)
			answer := agent.NativeSessionApprovalAnswer{SessionID: controlTestSession, BindingToken: state.BindingToken,
				RequestID: "generated-permission-answer", PromptID: prompt.PromptID, OptionID: option, ExpectedTurnID: frame.ActiveTurn}
			completed := make(chan error, 1)
			go func() { completed <- c.AnswerApproval(t.Context(), answer) }()
			body := <-result
			var command nativeControlCommand
			require.NoError(t, json.Unmarshal(body["command"], &command))
			require.Equal(t, "approval", command.Kind)
			require.Equal(t, option, command.OptionID)
			require.Equal(t, prompt.PromptID, command.PromptID)
			poll := frame
			poll.Approval = nil
			poll.Outcomes = []nativeControlOutcome{{RequestID: command.RequestID, DeliveryID: command.DeliveryID,
				State: "answered", TurnID: command.TurnID}}
			controlTestPost(t, c, "/poll", poll, "", c.token)
			require.NoError(t, <-completed)
			require.NoError(t, c.AnswerApproval(t.Context(), answer), "an identical request returns its existing receipt")
			answer.OptionID = "allow_once"
			if option == "allow_once" {
				answer.OptionID = "deny"
			}
			require.ErrorIs(t, c.AnswerApproval(t.Context(), answer), agent.ErrNativeControlInvalid)
			answer.RequestID, answer.OptionID = "new-browser-request", "deny"
			require.ErrorIs(t, c.AnswerApproval(t.Context(), answer), agent.ErrNativeControlStale,
				"a browser reload cannot answer the same invocation again")
			state, err = c.Inspect(t.Context(), controlTestSession)
			require.NoError(t, err)
			require.Empty(t, state.PendingApprovals)
			require.Contains(t, state.RecentInputs[0].Detail, "does not confirm tool execution")
		})
	}
}

func TestNativeControlApprovalRenewsSameInvocationAndOriginalToken(t *testing.T) {
	c, clock := controlTestController(t)
	controlTestRegister(t, c)
	frame := controlTestApprovalFrame()
	result := controlTestHoldApproval(t, c, clock, frame)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	id := state.PendingApprovals[0].PromptID
	clock.Advance(nativeControlApprovalPoll).MustWait(testutil.DeadlineContext(t))
	body := <-result
	require.JSONEq(t, "true", string(body["waiting"]))
	// Repeated bounded waits retain one request and one immutable public token.
	result = controlTestHoldApproval(t, c, clock, frame)
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.Len(t, state.PendingApprovals, 1)
	require.Equal(t, id, state.PendingApprovals[0].PromptID)
	poll := frame
	poll.Approval = nil
	poll.ApprovalIDs = nil
	controlTestPost(t, c, "/poll", poll, "", c.token)
	body = <-result
	require.JSONEq(t, "true", string(body["fallback"]))
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.Empty(t, state.PendingApprovals)
}

func TestNativeControlApprovalChangedPayloadAndConcurrentAnswerRefused(t *testing.T) {
	c, clock := controlTestController(t)
	controlTestRegister(t, c)
	frame := controlTestApprovalFrame()
	result := controlTestHoldApproval(t, c, clock, frame)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	id := state.PendingApprovals[0].PromptID
	changed := frame
	copy := *frame.Approval
	copy.Detail = `{"input":"changed harmless generated input"}`
	changed.Approval = &copy
	status, _ := controlTestPost(t, c, "/approval", changed, "", c.token)
	require.Equal(t, http.StatusConflict, status)
	command, err := c.enqueue(t.Context(), controlTestSession, state.BindingToken, &nativeControlCommand{
		RequestID: "first-explicit-answer", Kind: "approval", TurnID: frame.ActiveTurn, PromptID: id, OptionID: "deny"})
	require.NoError(t, err)
	_, err = c.enqueue(t.Context(), controlTestSession, state.BindingToken, &nativeControlCommand{
		RequestID: "racing-explicit-answer", Kind: "approval", TurnID: frame.ActiveTurn, PromptID: id, OptionID: "allow_once"})
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
	<-result
	// Losing the hook's acknowledgement is uncertain and must not enable a new
	// request ID to resend a different permission answer.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = c.AnswerApproval(ctx, agent.NativeSessionApprovalAnswer{SessionID: controlTestSession,
		BindingToken: state.BindingToken, RequestID: "after-unknown", PromptID: id, OptionID: "allow_once", ExpectedTurnID: frame.ActiveTurn})
	require.Error(t, err)
	c.mu.Lock()
	require.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DISPATCHED, command.state)
	c.mu.Unlock()
	err = c.AnswerApproval(t.Context(), agent.NativeSessionApprovalAnswer{SessionID: controlTestSession,
		BindingToken: state.BindingToken, RequestID: "after-unknown-fresh", PromptID: id, OptionID: "allow_once", ExpectedTurnID: frame.ActiveTurn})
	require.ErrorIs(t, err, agent.ErrNativeControlStale)
}

func TestNativeControlApprovalStaysLiveWithoutBrowserInspection(t *testing.T) {
	c, clock := controlTestController(t)
	controlTestRegister(t, c)
	frame := controlTestApprovalFrame()
	result := controlTestHoldApproval(t, c, clock, frame)
	state, err := c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	id := state.PendingApprovals[0].PromptID
	// Forty seconds exceeds the browser lease. Only genuine successful native
	// owner checks and this plugin's independent polls run during this interval.
	for range 8 {
		clock.Advance(nativeControlApprovalPoll).MustWait(testutil.DeadlineContext(t))
		body := <-result
		require.JSONEq(t, "true", string(body["waiting"]))
		result = controlTestHoldApproval(t, c, clock, frame)
	}
	state, err = c.Inspect(t.Context(), controlTestSession)
	require.NoError(t, err)
	require.Len(t, state.PendingApprovals, 1)
	require.Equal(t, id, state.PendingApprovals[0].PromptID)
	require.True(t, state.PendingApprovals[0].CanAnswer)
	poll := frame
	poll.Approval, poll.ApprovalIDs = nil, nil
	controlTestPost(t, c, "/poll", poll, "", c.token)
	body := <-result
	require.JSONEq(t, "true", string(body["fallback"]))
}

func TestNativeControlApprovalCancelledTurnReloadAndBounds(t *testing.T) {
	for _, cancel := range []string{"local_resolution", "turn_changed", "local_stop", "plugin_reload"} {
		t.Run(cancel, func(t *testing.T) {
			c, clock := controlTestController(t)
			controlTestRegister(t, c)
			frame := controlTestApprovalFrame()
			result := controlTestHoldApproval(t, c, clock, frame)
			state, err := c.Inspect(t.Context(), controlTestSession)
			require.NoError(t, err)
			id := state.PendingApprovals[0].PromptID
			poll := frame
			poll.Approval = nil
			switch cancel {
			case "local_resolution":
				poll.ApprovalIDs = nil
			case "turn_changed":
				poll.ActiveTurn = "new-generated-turn"
			case "local_stop":
				poll.Stopped = true
			case "plugin_reload":
				poll.InstanceID = "new-generated-instance"
				controlTestPost(t, c, "/register", poll, "", c.token)
			}
			if cancel != "plugin_reload" {
				controlTestPost(t, c, "/poll", poll, "", c.token)
			}
			body := <-result
			require.JSONEq(t, "true", string(body["fallback"]))
			err = c.AnswerApproval(t.Context(), agent.NativeSessionApprovalAnswer{SessionID: controlTestSession,
				BindingToken: state.BindingToken, RequestID: "stale-answer", PromptID: id, OptionID: "allow_once", ExpectedTurnID: frame.ActiveTurn})
			require.Error(t, err)
		})
	}
	c, _ := controlTestController(t)
	controlTestRegister(t, c)
	for _, field := range []string{"protocol", "detail", "title", "turn", "nul"} {
		frame := controlTestApprovalFrame()
		switch field {
		case "protocol":
			frame.Approval.Protocol = 2
		case "detail":
			frame.Approval.Detail = strings.Repeat("x", contracts.NativeSessionControlMaxApprovalDetailBytes+1)
		case "title":
			frame.Approval.Title = strings.Repeat("x", contracts.NativeSessionControlMaxApprovalTextBytes+1)
		case "turn":
			frame.Approval.TurnID = "different-generated-turn"
		case "nul":
			frame.Approval.Detail = "generated\x00content"
		}
		status, _ := controlTestPost(t, c, "/approval", frame, "", c.token)
		require.Equal(t, http.StatusBadRequest, status, field)
	}
}

func TestNativeControlApprovalTokenBindsOwnerGenerationAndEntireDetail(t *testing.T) {
	c, _ := controlTestController(t)
	controlTestRegister(t, c)
	frame := controlTestApprovalFrame()
	c.mu.Lock()
	defer c.mu.Unlock()
	peer := c.peers[controlTestSession]
	original := c.approvalToken(peer, frame.Approval)
	frame.Approval.Detail += " "
	require.NotEqual(t, original, c.approvalToken(peer, frame.Approval))
	frame.Approval.Detail = strings.TrimSuffix(frame.Approval.Detail, " ")
	peer.instance = "new-plugin-generation"
	require.NotEqual(t, original, c.approvalToken(peer, frame.Approval))
	peer.instance = controlTestInstance
	peer.owner.identity.StartTime++
	require.NotEqual(t, original, c.approvalToken(peer, frame.Approval))
}
