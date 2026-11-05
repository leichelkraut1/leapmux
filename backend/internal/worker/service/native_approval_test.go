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
)

func nativeApprovalState(now time.Time) agent.NativeSessionControlState {
	return agent.NativeSessionControlState{
		SessionID: "generated-session", OwnerPresent: true,
		BindingToken: "generated-owner-binding", ObservedAt: now, ExpiresAt: now.Add(30 * time.Second),
		PendingApprovals: []agent.NativeSessionApproval{{
			PromptID: "generated-prompt", TurnID: "generated-turn", Title: "Run generated command?",
			Detail: "Command: print harmless generated marker\nDirectory: generated temporary folder", CanAnswer: true,
			Options: []agent.NativeSessionApprovalOption{
				{ID: "allow-once", Label: "Allow once", Description: "Only this request."},
				{ID: "deny", Label: "Deny", Description: "Refuse this request."},
			},
		}},
	}
}

func TestNativeApprovalResponseAllowsAnswerOnlyLeaseAndPreservesReviewText(t *testing.T) {
	now := time.Now()
	state := nativeApprovalState(now)
	response, err := nativeControlResponse(state.SessionID, state, now)
	require.NoError(t, err)
	assert.False(t, response.CanSend)
	assert.False(t, response.CanInterrupt)
	require.Len(t, response.PendingApprovals, 1)
	prompt := response.PendingApprovals[0]
	assert.Equal(t, state.PendingApprovals[0].Detail, prompt.Detail)
	assert.True(t, prompt.CanAnswer)
	assert.Equal(t, "allow-once", prompt.Options[0].Id)
	state.ExpiresAt = now
	_, err = nativeControlResponse(state.SessionID, state, now)
	require.Error(t, err)
}

func TestNativeApprovalResponseRejectsAmbiguousOrIncompleteReviewData(t *testing.T) {
	cases := map[string]func(*agent.NativeSessionControlState){
		"duplicate prompt": func(s *agent.NativeSessionControlState) {
			s.PendingApprovals = append(s.PendingApprovals, s.PendingApprovals[0])
		},
		"unsafe prompt ID": func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].PromptID = "../another-prompt" },
		"unsafe turn ID":   func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].TurnID = "another/turn" },
		"missing title":    func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].Title = " " },
		"missing detail":   func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].Detail = "" },
		"oversize detail": func(s *agent.NativeSessionControlState) {
			s.PendingApprovals[0].Detail = strings.Repeat("x", contracts.NativeSessionControlMaxApprovalDetailBytes+1)
		},
		"invalid UTF8": func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].Detail = string([]byte{0xff}) },
		"NUL detail":   func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].Detail = "generated\x00hidden" },
		"contradictory capability": func(s *agent.NativeSessionControlState) {
			s.PendingApprovals[0].UnavailableReason = "Cannot answer this prompt."
		},
		"no choices": func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].Options = nil },
		"duplicate choice": func(s *agent.NativeSessionControlState) {
			p := &s.PendingApprovals[0]
			p.Options = append(p.Options, p.Options[0])
		},
		"invalid choice ID":    func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].Options[0].ID = "arbitrary/choice" },
		"missing choice label": func(s *agent.NativeSessionControlState) { s.PendingApprovals[0].Options[0].Label = " " },
		"oversize description": func(s *agent.NativeSessionControlState) {
			s.PendingApprovals[0].Options[0].Description = strings.Repeat("x", contracts.NativeSessionControlMaxApprovalTextBytes+1)
		},
		"too many prompts": func(s *agent.NativeSessionControlState) {
			s.PendingApprovals = make([]agent.NativeSessionApproval, contracts.NativeSessionControlMaxPendingApprovals+1)
		},
		"too many choices": func(s *agent.NativeSessionControlState) {
			s.PendingApprovals[0].Options = make([]agent.NativeSessionApprovalOption, contracts.NativeSessionControlMaxApprovalOptions+1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			state := nativeApprovalState(now)
			mutate(&state)
			_, err := nativeControlResponse(state.SessionID, state, now)
			require.Error(t, err)
		})
	}
}

func TestNativeApprovalResponseKeepsUnsupportedPromptReadOnly(t *testing.T) {
	now := time.Now()
	state := nativeApprovalState(now)
	state.BindingToken = ""
	state.ObservedAt, state.ExpiresAt = time.Time{}, time.Time{}
	state.PendingApprovals[0].CanAnswer = false
	state.PendingApprovals[0].Options = nil
	state.PendingApprovals[0].UnavailableReason = "Answer this request in its original app."
	response, err := nativeControlResponse(state.SessionID, state, now)
	require.NoError(t, err)
	assert.False(t, response.PendingApprovals[0].CanAnswer)
	assert.Empty(t, response.PendingApprovals[0].Options)
	assert.Empty(t, response.BindingToken)
}

func TestNativeApprovalRPCRejectsMalformedIdentityBeforeConstructingController(t *testing.T) {
	for _, change := range []func(*leapmuxv1.AnswerNativeSessionApprovalRequest){
		func(r *leapmuxv1.AnswerNativeSessionApprovalRequest) { r.PromptId = "../other" },
		func(r *leapmuxv1.AnswerNativeSessionApprovalRequest) { r.OptionId = "" },
		func(r *leapmuxv1.AnswerNativeSessionApprovalRequest) { r.OptionId = strings.Repeat("a", 129) },
		func(r *leapmuxv1.AnswerNativeSessionApprovalRequest) { r.ExpectedTurnId = "other/turn" },
	} {
		svc, dispatcher, writer := setupTestService(t)
		request := &leapmuxv1.AnswerNativeSessionApprovalRequest{
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, SessionId: "generated-session",
			BindingToken: "generated-owner-binding", RequestId: "generated-request", PromptId: "generated-prompt", OptionId: "allow-once",
		}
		change(request)
		dispatch(dispatcher, "AnswerNativeSessionApproval", request, writer)
		require.Len(t, writer.errors, 1)
		assert.Empty(t, writer.responses)
		assert.Empty(t, svc.Agents.ListAgentIDs())
	}
}
