package service

import (
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNativeControlValidatesIdentitiesAndUTF8ByteBounds(t *testing.T) {
	assert.True(t, validNativeControlMutation("generated-session", "generated-opaque-binding", "generated-request"))
	for _, session := range []string{"", "../other", `C:\other`, "other/session"} {
		assert.False(t, validNativeControlMutation(session, "generated-opaque-binding", "generated-request"))
	}
	assert.False(t, validNativeControlMutation("session", "missing", "generated-request"))
	assert.False(t, validNativeControlMutation("session", "generated-opaque-binding", ""))
	assert.True(t, validNativeControlText(strings.Repeat("a", contracts.NativeSessionControlMaxTextBytes)))
	assert.False(t, validNativeControlText(strings.Repeat("a", contracts.NativeSessionControlMaxTextBytes+1)))
	assert.False(t, validNativeControlText(strings.Repeat("界", contracts.NativeSessionControlMaxTextBytes/3+1)))
	for _, text := range []string{"", " \t\n", "marker\x00other", string([]byte{0xff})} {
		assert.False(t, validNativeControlText(text))
	}
}

func TestNativeControlResponseRejectsWrongOwnerExpiredLeaseAndStaleTurn(t *testing.T) {
	now := time.Now()
	state := agent.NativeSessionControlState{
		SessionID: "generated", BindingToken: "generated-opaque-binding", CanSend: true,
		InputBehavior: leapmuxv1.NativeSessionInputBehavior_NATIVE_SESSION_INPUT_BEHAVIOR_QUEUE_NEXT_TURN,
		ObservedAt:    now, ExpiresAt: now.Add(contracts.NativeSessionControlLeaseSeconds * time.Second),
	}
	_, err := nativeControlResponse("other-session", state, now)
	require.Error(t, err)
	_, err = nativeControlResponse("generated", state, state.ExpiresAt)
	require.Error(t, err)
	state.CanInterrupt = true
	_, err = nativeControlResponse("generated", state, now)
	require.Error(t, err)
	state.ActiveTurnID = "generated-turn"
	state.RecentInputs = []agent.NativeSessionInputOutcome{{RequestID: "generated-request", State: leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED, Detail: "Stopped before starting."}}
	response, err := nativeControlResponse("generated", state, now)
	require.NoError(t, err)
	require.Len(t, response.RecentInputs, 1)
	assert.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_DROPPED, response.RecentInputs[0].State)
	state.RecentInputs[0].State = leapmuxv1.NativeSessionInputState(999)
	_, err = nativeControlResponse("generated", state, now)
	require.Error(t, err)
}

func TestNativeControlDebugLoggingOmitsMessageAndBinding(t *testing.T) {
	for _, message := range []proto.Message{
		&leapmuxv1.SendNativeSessionInputRequest{Text: "private generated marker", BindingToken: "private binding"},
		&leapmuxv1.InterruptNativeSessionRequest{BindingToken: "private binding"},
		&leapmuxv1.InspectNativeSessionControlResponse{BindingToken: "private binding"},
	} {
		logged := lazyProtoJSON(message).LogValue().String()
		assert.NotContains(t, logged, "private")
	}
}

func TestNativeControlUnavailableRPCPreservesOnlyTheRequestedSessionsDeliveryOutcomes(t *testing.T) {
	for _, returnedSession := range []string{"generated-session", "another-session"} {
		t.Run(returnedSession, func(t *testing.T) {
			svc, dispatcher, writer := setupTestService(t)
			fixture := &nativeAdmissionController{err: agent.ErrNativeControlUnavailable, state: agent.NativeSessionControlState{
				SessionID: returnedSession, OwnerLabel: "Generated native owner", BindingToken: "generated-old-binding",
				CanSend: true, CanInterrupt: true, ActiveTurnID: "generated-turn", ExpiresAt: time.Now().Add(time.Minute),
				RecentInputs: []agent.NativeSessionInputOutcome{{RequestID: "generated-request", State: leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN, Detail: "Delivery could not be confirmed."}},
			}}
			installNativeAdmissionFixture(t, svc, fixture)
			dispatch(dispatcher, "InspectNativeSessionControl", &leapmuxv1.InspectNativeSessionControlRequest{
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, SessionId: "generated-session",
			}, writer)
			require.Empty(t, writer.errors)
			require.Len(t, writer.responses, 1)
			response := &leapmuxv1.InspectNativeSessionControlResponse{}
			require.NoError(t, proto.Unmarshal(writer.responses[0].Payload, response))
			assert.Equal(t, "generated-session", response.SessionId)
			assert.False(t, response.CanSend)
			assert.False(t, response.CanInterrupt)
			assert.Empty(t, response.BindingToken)
			assert.Empty(t, response.ActiveTurnId)
			assert.Empty(t, response.ExpiresAt)
			if returnedSession == "generated-session" {
				require.Len(t, response.RecentInputs, 1)
				assert.Equal(t, leapmuxv1.NativeSessionInputState_NATIVE_SESSION_INPUT_STATE_UNCERTAIN, response.RecentInputs[0].State)
			} else {
				assert.Empty(t, response.RecentInputs)
				assert.Empty(t, response.OwnerLabel)
			}
		})
	}
}
