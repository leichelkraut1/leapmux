package codexobserve

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func generatedApproval(method string, id any, extra map[string]any) controlApprovalRequest {
	params := map[string]any{"threadId": controlTestThread, "turnId": controlTestTurn, "itemId": "generated-item",
		"reason": "Harmless generated approval fixture", "command": "echo GENERATED_APPROVAL_OUTPUT", "cwd": "/generated"}
	for key, value := range extra {
		params[key] = value
	}
	raw, _ := json.Marshal(params)
	identifier, _ := json.Marshal(id)
	return controlApprovalRequest{ID: identifier, Method: method, Params: raw}
}

func approvalFixture(t *testing.T, requests ...controlApprovalRequest) (*Controller, *controlTestServer, agent.NativeSessionControlState, agent.NativeSessionControllerOptions, controlDependencies) {
	t.Helper()
	c, server, options, dependencies := controlFixture(t)
	server.mu.Lock()
	server.active = true
	server.runtimeFlags = []string{"waitingOnApproval"}
	server.requests = requests
	for _, request := range requests {
		if request.Method == "item/fileChange/requestApproval" {
			server.approvalItems = append(server.approvalItems, json.RawMessage(`{"id":"generated-item","type":"fileChange","status":"inProgress","changes":[{"path":"/generated/example.txt","kind":{"type":"add"},"diff":"+GENERATED_FILE_CHANGE"}]}`))
		}
	}
	server.mu.Unlock()
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.True(t, state.CanSend)
	require.Len(t, state.PendingApprovals, len(requests))
	return c, server, state, options, dependencies
}

func approvalAnswer(state agent.NativeSessionControlState, option string) agent.NativeSessionApprovalAnswer {
	return agent.NativeSessionApprovalAnswer{SessionID: controlTestThread, BindingToken: state.BindingToken,
		RequestID: "77777777-7777-4777-8777-777777777777", PromptID: state.PendingApprovals[0].PromptID,
		ExpectedTurnID: state.PendingApprovals[0].TurnID, OptionID: option}
}

func TestNativeControlApprovalExactOwnerAndNativeRequestID(t *testing.T) {
	for _, test := range []struct {
		name, method, wire, option, decision string
		id                                   any
	}{
		{name: "command-numeric", method: "item/commandExecution/requestApproval", wire: "thread-follower-command-approval-decision", option: "allow_once", decision: "accept", id: 19},
		{name: "command-string", method: "item/commandExecution/requestApproval", wire: "thread-follower-command-approval-decision", option: "deny", decision: "decline", id: "approval-19"},
		{name: "file", method: "item/fileChange/requestApproval", wire: "thread-follower-file-approval-decision", option: "allow_once", decision: "accept", id: "file-19"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := generatedApproval(test.method, test.id, nil)
			c, server, state, options, _ := approvalFixture(t, request)
			prompt := state.PendingApprovals[0]
			require.True(t, prompt.CanAnswer)
			require.Regexp(t, "^[a-f0-9]{64}$", prompt.PromptID)
			require.Contains(t, prompt.Detail, "echo GENERATED_APPROVAL_OUTPUT")
			require.NoError(t, c.AnswerApproval(context.Background(), approvalAnswer(state, test.option)))
			require.NoError(t, c.AnswerApproval(context.Background(), approvalAnswer(state, test.option)))
			server.mu.Lock()
			require.Equal(t, 1, server.mutations)
			require.Equal(t, []string{test.wire}, server.approvalMethods)
			require.Equal(t, []string{controlTestOwner}, server.targets)
			require.Equal(t, []string{controlTestThread}, server.threads)
			require.JSONEq(t, string(request.ID), string(server.approvalParams[0]["requestId"]))
			require.Equal(t, test.decision, testString(server.approvalParams[0]["decision"]))
			server.mu.Unlock()
			files, err := os.ReadDir(options.Directory)
			require.NoError(t, err)
			for _, file := range files {
				raw, err := os.ReadFile(filepath.Join(options.Directory, file.Name()))
				require.NoError(t, err)
				require.NotContains(t, string(raw), "GENERATED_APPROVAL_OUTPUT")
				require.NotContains(t, string(raw), "PRIVATE_GENERATED_TRANSCRIPT")
			}
		})
	}
}

func TestNativeControlApprovalOfferedChoicesAndCompleteDetails(t *testing.T) {
	request := generatedApproval("item/commandExecution/requestApproval", "approval", map[string]any{
		"availableDecisions":    []any{"decline", "acceptForSession", map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []string{"echo"}}}},
		"additionalPermissions": map[string]any{"network": map[string]any{"enabled": true}},
	})
	_, _, state, _, _ := approvalFixture(t, request)
	prompt := state.PendingApprovals[0]
	require.True(t, prompt.CanAnswer)
	require.Len(t, prompt.Options, 1)
	require.Equal(t, "deny", prompt.Options[0].ID)
	require.Contains(t, prompt.Detail, "additionalPermissions")
	require.Contains(t, prompt.Detail, "execpolicy_amendment")
}

func TestNativeControlApprovalUsesNativeCancelWhenDeclineIsNotOffered(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval"} {
		t.Run(method, func(t *testing.T) {
			request := generatedApproval(method, 51, map[string]any{
				"availableDecisions": []any{"accept", map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []string{"echo"}}}, "cancel"},
			})
			c, server, state, _, _ := approvalFixture(t, request)
			prompt := state.PendingApprovals[0]
			require.Len(t, prompt.Options, 2)
			require.Equal(t, "allow_once", prompt.Options[0].ID)
			require.Equal(t, "cancel", prompt.Options[1].ID)
			require.Contains(t, prompt.Options[1].Description, "stop its current turn")
			require.ErrorIs(t, c.AnswerApproval(t.Context(), approvalAnswer(state, "deny")), agent.ErrNativeControlInvalid)
			answer := approvalAnswer(state, "cancel")
			answer.RequestID = "88888888-8888-4888-8888-888888888888"
			require.NoError(t, c.AnswerApproval(t.Context(), answer))
			require.NoError(t, c.AnswerApproval(t.Context(), answer))
			server.mu.Lock()
			defer server.mu.Unlock()
			require.Equal(t, 1, server.mutations)
			require.Equal(t, "cancel", testString(server.approvalParams[0]["decision"]))
		})
	}
}

func TestNativeControlApprovalRejectsChangedPromptOwnerTurnAndChoice(t *testing.T) {
	for _, change := range []string{"changed-content", "resolved", "changed-owner", "changed-turn", "unoffered-choice", "duplicate-native-id", "equivalent-escaped-id"} {
		t.Run(change, func(t *testing.T) {
			request := generatedApproval("item/commandExecution/requestApproval", "approval", nil)
			c, server, state, _, _ := approvalFixture(t, request)
			answer := approvalAnswer(state, "allow_once")
			server.mu.Lock()
			switch change {
			case "changed-content":
				server.requests[0] = generatedApproval(request.Method, "approval", map[string]any{"command": "echo DIFFERENT_GENERATED_COMMAND"})
			case "resolved":
				server.requests = nil
			case "changed-owner":
				server.owner = "66666666-6666-4666-8666-666666666666"
			case "changed-turn":
				answer.ExpectedTurnID = "66666666-6666-4666-8666-666666666666"
			case "unoffered-choice":
				answer.OptionID = "accept_for_session"
			case "duplicate-native-id":
				server.requests = append(server.requests, generatedApproval(request.Method, "approval", map[string]any{"command": "echo DIFFERENT_GENERATED_COMMAND"}))
			case "equivalent-escaped-id":
				duplicate := generatedApproval(request.Method, "approval", map[string]any{"command": "echo DIFFERENT_GENERATED_COMMAND"})
				duplicate.ID = json.RawMessage(`"\u0061pproval"`)
				server.requests = append(server.requests, duplicate)
			}
			server.mu.Unlock()
			err := c.AnswerApproval(context.Background(), answer)
			require.Error(t, err)
			server.mu.Lock()
			require.Zero(t, server.mutations)
			server.mu.Unlock()
		})
	}
}

func TestNativeControlApprovalUnsupportedAndOversizeRemainReadOnly(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		extra  map[string]any
	}{
		{name: "unknown-kind", method: "future/requestApproval"},
		{name: "user-input", method: "item/tool/requestUserInput"},
		{name: "no-offered-once-choice", method: "item/commandExecution/requestApproval", extra: map[string]any{"availableDecisions": []string{"acceptForSession"}}},
		{name: "foreign-thread", method: "item/commandExecution/requestApproval", extra: map[string]any{"threadId": "66666666-6666-4666-8666-666666666666"}},
		{name: "stale-turn", method: "item/commandExecution/requestApproval", extra: map[string]any{"turnId": "66666666-6666-4666-8666-666666666666"}},
		{name: "oversize-detail", method: "item/commandExecution/requestApproval", extra: map[string]any{"command": strings.Repeat("x", contracts.NativeSessionControlMaxApprovalDetailBytes)}},
		{name: "missing-command", method: "item/commandExecution/requestApproval", extra: map[string]any{"command": nil}},
		{name: "missing-network-destination", method: "item/commandExecution/requestApproval", extra: map[string]any{"command": nil, "networkApprovalContext": map[string]any{"protocol": "https"}}},
		{name: "future-permissions", method: "item/permissions/requestApproval", extra: map[string]any{"permissions": map[string]any{"futureGrant": true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, state, _, _ := approvalFixture(t, generatedApproval(test.method, "approval", test.extra))
			prompt := state.PendingApprovals[0]
			require.False(t, prompt.CanAnswer)
			require.NotEmpty(t, prompt.UnavailableReason)
			require.LessOrEqual(t, len(prompt.Detail), contracts.NativeSessionControlMaxApprovalDetailBytes)
		})
	}
}

func TestNativeControlApprovalNetworkDestinationIsFullyVisible(t *testing.T) {
	request := generatedApproval("item/commandExecution/requestApproval", "network-approval", map[string]any{
		"command": nil, "networkApprovalContext": map[string]any{"host": "example.invalid", "protocol": "https", "port": 443},
	})
	_, _, state, _, _ := approvalFixture(t, request)
	prompt := state.PendingApprovals[0]
	require.True(t, prompt.CanAnswer)
	require.Equal(t, "Approve network access", prompt.Title)
	require.Contains(t, prompt.Detail, "example.invalid")
	require.Contains(t, prompt.Detail, "https")
	require.Contains(t, prompt.Detail, "443")
}

func TestNativeControlApprovalExactRequestedPermissionsTurnOnly(t *testing.T) {
	permissions := map[string]any{"network": map[string]any{"enabled": true}, "fileSystem": map[string]any{"read": []string{"/generated/read"}, "write": []string{"/generated/write"}}}
	for _, choice := range []string{"allow_turn", "deny"} {
		t.Run(choice, func(t *testing.T) {
			request := generatedApproval("item/permissions/requestApproval", 12, map[string]any{"permissions": permissions})
			c, server, state, _, _ := approvalFixture(t, request)
			require.True(t, state.PendingApprovals[0].CanAnswer)
			require.NoError(t, c.AnswerApproval(context.Background(), approvalAnswer(state, choice)))
			server.mu.Lock()
			require.Equal(t, []string{"thread-follower-permissions-request-approval-response"}, server.approvalMethods)
			var response struct {
				Permissions map[string]any `json:"permissions"`
				Scope       string         `json:"scope"`
			}
			require.NoError(t, json.Unmarshal(server.approvalParams[0]["response"], &response))
			require.Equal(t, "turn", response.Scope)
			if choice == "deny" {
				require.Empty(t, response.Permissions)
			} else {
				actual, err := json.Marshal(response.Permissions)
				require.NoError(t, err)
				expected, err := json.Marshal(permissions)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(actual))
			}
			server.mu.Unlock()
		})
	}
}

func TestNativeControlApprovalFileDiffIsCompleteAndPinned(t *testing.T) {
	request := generatedApproval("item/fileChange/requestApproval", "file-approval", nil)
	c, server, state, _, _ := approvalFixture(t, request)
	require.Contains(t, state.PendingApprovals[0].Detail, "/generated/example.txt")
	require.Contains(t, state.PendingApprovals[0].Detail, "+GENERATED_FILE_CHANGE")
	require.NotContains(t, state.PendingApprovals[0].Detail, "PRIVATE_GENERATED_TRANSCRIPT")
	server.mu.Lock()
	server.approvalItems[0] = json.RawMessage(`{"id":"generated-item","type":"fileChange","status":"inProgress","changes":[{"path":"/generated/example.txt","kind":{"type":"add"},"diff":"+DIFFERENT_GENERATED_CHANGE"}]}`)
	server.mu.Unlock()
	require.ErrorIs(t, c.AnswerApproval(context.Background(), approvalAnswer(state, "allow_once")), agent.ErrNativeControlStale)
	server.mu.Lock()
	require.Zero(t, server.mutations)
	server.approvalItems = nil
	server.mu.Unlock()
	missing, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.False(t, missing.PendingApprovals[0].CanAnswer)
	require.Contains(t, missing.PendingApprovals[0].UnavailableReason, "complete proposed file changes")
}

func TestNativeControlApprovalUncertainNeverReplayedAcrossRestart(t *testing.T) {
	request := generatedApproval("item/commandExecution/requestApproval", "approval", nil)
	c, server, state, options, dependencies := approvalFixture(t, request)
	server.mu.Lock()
	server.cut = true
	server.mu.Unlock()
	answer := approvalAnswer(state, "allow_once")
	require.ErrorIs(t, c.AnswerApproval(context.Background(), answer), agent.ErrNativeControlUncertain)
	require.ErrorIs(t, c.AnswerApproval(context.Background(), answer), agent.ErrNativeControlUncertain)
	changedID := answer
	changedID.RequestID = "88888888-8888-4888-8888-888888888888"
	changedID.OptionID = "deny"
	require.ErrorIs(t, c.AnswerApproval(context.Background(), changedID), agent.ErrNativeControlUncertain)
	uncertainState, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.False(t, uncertainState.PendingApprovals[0].CanAnswer)
	require.Contains(t, uncertainState.PendingApprovals[0].UnavailableReason, "will not be resent")
	require.NoError(t, c.Close())
	replacement := newController(options, dependencies)
	t.Cleanup(func() { require.NoError(t, replacement.Close()) })
	require.ErrorIs(t, replacement.AnswerApproval(context.Background(), answer), agent.ErrNativeControlUncertain)
	current, err := replacement.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.False(t, current.PendingApprovals[0].CanAnswer)
	changedID = approvalAnswer(current, "allow_once")
	changedID.RequestID = "99999999-9999-4999-8999-999999999999"
	require.ErrorIs(t, replacement.AnswerApproval(context.Background(), changedID), agent.ErrNativeControlUncertain)
	server.mu.Lock()
	require.Equal(t, 1, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlApprovalPromptGenerationChangesWithOwnerAndEpoch(t *testing.T) {
	request := generatedApproval("item/commandExecution/requestApproval", "approval", nil)
	c, server, state, options, dependencies := approvalFixture(t, request)
	server.mu.Lock()
	server.owner = "66666666-6666-4666-8666-666666666666"
	server.mu.Unlock()
	changed, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.NotEqual(t, state.PendingApprovals[0].PromptID, changed.PendingApprovals[0].PromptID)
	require.NoError(t, c.Close())
	replacement := newController(options, dependencies)
	t.Cleanup(func() { require.NoError(t, replacement.Close()) })
	current, err := replacement.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.NotEqual(t, changed.PendingApprovals[0].PromptID, current.PendingApprovals[0].PromptID)
}

func TestNativeControlApprovalAcceptedPendingNeverReplayedAfterLeaseAndRestart(t *testing.T) {
	request := generatedApproval("item/commandExecution/requestApproval", "approval", nil)
	c, server, state, options, dependencies := approvalFixture(t, request)
	now := state.ObservedAt
	c.deps.now = func() time.Time { return now }
	dependencies.now = c.deps.now
	require.NoError(t, c.AnswerApproval(context.Background(), approvalAnswer(state, "allow_once")))
	// The owner deliberately retains its pending request after the routing ACK.
	// Expiring the original lease must not make that prompt answerable again.
	now = state.ExpiresAt.Add(24 * time.Hour)
	fresh, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.True(t, fresh.CanSend)
	require.False(t, fresh.PendingApprovals[0].CanAnswer)
	require.Contains(t, fresh.PendingApprovals[0].UnavailableReason, "already submitted")
	answer := approvalAnswer(fresh, "deny")
	answer.RequestID = "88888888-8888-4888-8888-888888888888"
	require.ErrorIs(t, c.AnswerApproval(context.Background(), answer), agent.ErrNativeControlStale)
	require.NoError(t, c.Close())
	replacement := newController(options, dependencies)
	t.Cleanup(func() { require.NoError(t, replacement.Close()) })
	current, err := replacement.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.NotEqual(t, fresh.PendingApprovals[0].PromptID, current.PendingApprovals[0].PromptID)
	require.False(t, current.PendingApprovals[0].CanAnswer)
	answer = approvalAnswer(current, "allow_once")
	answer.RequestID = "99999999-9999-4999-8999-999999999999"
	require.ErrorIs(t, replacement.AnswerApproval(context.Background(), answer), agent.ErrNativeControlStale)
	server.mu.Lock()
	require.Equal(t, 1, server.mutations)
	server.mu.Unlock()
}

func TestNativeControlApprovalPostWriteReadDeadlineIsUncertain(t *testing.T) {
	for _, method := range []string{"thread-follower-command-approval-decision", "thread-follower-file-approval-decision", "thread-follower-permissions-request-approval-response"} {
		t.Run(method, func(t *testing.T) {
			conn := &controlPostWriteDeadlineFailure{}
			wire := &controlConnection{conn: conn, client: controlTestThread, owner: controlTestOwner,
				verify: func(context.Context, net.Conn, PeerIdentity) error { return nil }}
			_, err := wire.request(context.Background(), method, 1, map[string]string{}, controlTestOwner)
			require.ErrorIs(t, err, agent.ErrNativeControlUncertain)
			require.Equal(t, 2, conn.writes)
		})
	}
}

func TestNativeControlApprovalNativeIDValidationAndOverflow(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `{}`, `[]`, `1.5`, `-1`, `9007199254740992`, `""`, `"bad\u0000id"`} {
		t.Run(raw, func(t *testing.T) { require.False(t, controlApprovalID(json.RawMessage(raw))) })
	}
	for _, raw := range []string{`0`, `12`, `9007199254740991`, `"native-request"`} {
		t.Run(raw, func(t *testing.T) { require.True(t, controlApprovalID(json.RawMessage(raw))) })
	}
	c, server, _, _ := controlFixture(t)
	server.mu.Lock()
	server.active = true
	for range contracts.NativeSessionControlMaxPendingApprovals + 1 {
		server.requests = append(server.requests, generatedApproval("item/commandExecution/requestApproval", "approval", nil))
	}
	server.mu.Unlock()
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.Len(t, state.PendingApprovals, 1)
	require.False(t, state.PendingApprovals[0].CanAnswer)
	require.Contains(t, state.PendingApprovals[0].UnavailableReason, "too many")
}
