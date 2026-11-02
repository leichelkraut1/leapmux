package codexobserve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func TestNativeControlApprovalReviewStartsWithCompleteCommand(t *testing.T) {
	command := "Write-Output 'GENERATED_REVIEW_FIRST'\nWrite-Output 'GENERATED_REVIEW_LAST'\n"
	request := generatedApproval("item/commandExecution/requestApproval", "routing-request", map[string]any{
		"command": command, "startedAtMs": 1234567890, "environmentId": "routing-environment", "kind": "routing-kind",
		"availableDecisions":          []any{"accept", "cancel", map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []string{"pwsh", "generated"}}}},
		"additionalPermissions":       map[string]any{"fileSystem": map[string]any{"write": []string{"/generated/additional-scope"}}, "kind": "MEANINGFUL_NESTED_PERMISSION_KIND"},
		"proposedExecpolicyAmendment": []string{"pwsh", "generated"},
		"futurePermissionField":       map[string]any{"environmentId": "MEANINGFUL_NESTED_SCOPE", "scope": "REQUESTED_SCOPE"},
	})
	_, _, state, _, _ := approvalFixture(t, request)
	review := state.PendingApprovals[0].Detail
	require.True(t, state.PendingApprovals[0].CanAnswer)
	require.True(t, strings.HasPrefix(review, "Command\n"+command))
	require.Contains(t, review, "Working directory\n/generated")
	require.Contains(t, review, "Reason\nHarmless generated approval fixture")
	require.Less(t, strings.Index(review, "GENERATED_REVIEW_FIRST"), strings.Index(review, "Working directory"))
	require.Less(t, strings.Index(review, "Working directory"), strings.Index(review, "additionalPermissions"))
	for _, meaningful := range []string{"/generated/additional-scope", "proposedExecpolicyAmendment", "execpolicy_amendment", "MEANINGFUL_NESTED_PERMISSION_KIND", "futurePermissionField", "MEANINGFUL_NESTED_SCOPE", "REQUESTED_SCOPE"} {
		require.Contains(t, review, meaningful)
	}
	for _, routing := range []string{"routing-request", "routing-environment", "routing-kind", "threadId", "turnId", "itemId", "startedAtMs", "availableDecisions", request.Method} {
		require.NotContains(t, review, routing)
	}
}

func TestNativeControlApprovalReviewDeterministicAndExactAtByteLimit(t *testing.T) {
	request := generatedApproval("item/commandExecution/requestApproval", "approval", nil)
	raw, err := controlApprovalReview(request, nil)
	require.NoError(t, err)
	var params map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(request.Params, &params))
	fields := []string{"reason", "turnId", "threadId", "itemId", "cwd", "command"}
	var reordered strings.Builder
	reordered.WriteByte('{')
	for index, key := range fields {
		if index != 0 {
			reordered.WriteByte(',')
		}
		reordered.WriteString(`"` + key + `":`)
		reordered.Write(params[key])
	}
	reordered.WriteByte('}')
	request.Params = json.RawMessage(reordered.String())
	again, err := controlApprovalReview(request, nil)
	require.NoError(t, err)
	require.Equal(t, raw, again)
	// Formatting must preserve the complete action; exactly the advertised
	// byte limit is answerable, and one further byte disables the whole prompt.
	baseCommand := "echo GENERATED_APPROVAL_OUTPUT"
	command := baseCommand + strings.Repeat("x", contracts.NativeSessionControlMaxApprovalDetailBytes-len(raw))
	_, _, atLimit, _, _ := approvalFixture(t, generatedApproval(request.Method, "at-limit", map[string]any{"command": command}))
	require.True(t, atLimit.PendingApprovals[0].CanAnswer)
	require.Equal(t, contracts.NativeSessionControlMaxApprovalDetailBytes, len(atLimit.PendingApprovals[0].Detail))
	_, _, overLimit, _, _ := approvalFixture(t, generatedApproval(request.Method, "over-limit", map[string]any{"command": command + "x"}))
	require.False(t, overLimit.PendingApprovals[0].CanAnswer)
	require.Contains(t, overLimit.PendingApprovals[0].UnavailableReason, "complete prompt")
}

func TestNativeControlApprovalReviewPermissionsAndNetworkRemainComplete(t *testing.T) {
	permissions := map[string]any{"network": map[string]any{"enabled": true}, "fileSystem": map[string]any{"read": []string{"/generated/read"}, "write": []string{"/generated/write"}}}
	_, _, state, _, _ := approvalFixture(t, generatedApproval("item/permissions/requestApproval", "permission-routing", map[string]any{"permissions": permissions}))
	review := state.PendingApprovals[0].Detail
	require.True(t, strings.HasPrefix(review, "Requested permissions\n"))
	for _, scope := range []string{"/generated/read", "/generated/write", `"enabled": true`} {
		require.Contains(t, review, scope)
	}
	require.NotContains(t, review, "permission-routing")
	_, _, network, _, _ := approvalFixture(t, generatedApproval("item/commandExecution/requestApproval", "network-routing", map[string]any{
		"command": nil, "networkApprovalContext": map[string]any{"host": "generated.example.invalid", "protocol": "https", "port": 8443, "futureScope": "EXACT_DESTINATION_SCOPE"},
	}))
	review = network.PendingApprovals[0].Detail
	require.True(t, strings.HasPrefix(review, "Network access\n"))
	for _, destination := range []string{"generated.example.invalid", "https", "8443", "EXACT_DESTINATION_SCOPE"} {
		require.Contains(t, review, destination)
	}
}

func TestNativeControlApprovalReviewFileChangesKeepFullDiffAndMoveScope(t *testing.T) {
	request := generatedApproval("item/fileChange/requestApproval", "file-routing", map[string]any{"grantRoot": "/generated/grant-root"})
	c, server, _, _, _ := approvalFixture(t, request)
	server.mu.Lock()
	server.approvalItems[0] = json.RawMessage(`{"id":"generated-item","type":"fileChange","status":"inProgress","startedAtMs":123456,"changes":[{"path":"/generated/old.txt","kind":{"type":"update","move_path":"/generated/new.txt"},"diff":"--- old\n+++ new\n-old line\n+new line\n","futureFileScope":{"kind":"KEEP_NESTED_KIND"}},{"path":"/generated/second.txt","kind":{"type":"add"},"diff":"+second file\n"}],"futureFilePermission":"KEEP_FILE_SCOPE"}`)
	server.mu.Unlock()
	state, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	review := state.PendingApprovals[0].Detail
	require.True(t, state.PendingApprovals[0].CanAnswer)
	require.True(t, strings.HasPrefix(review, "File\n/generated/old.txt"))
	require.Contains(t, review, "Diff\n--- old\n+++ new\n-old line\n+new line\n")
	for _, meaningful := range []string{"/generated/new.txt", "/generated/second.txt", "+second file\n", "/generated/grant-root", "KEEP_NESTED_KIND", "KEEP_FILE_SCOPE"} {
		require.Contains(t, review, meaningful)
	}
	for _, metadata := range []string{"file-routing", "generated-item", "startedAtMs", `"inProgress"`, request.Method} {
		require.NotContains(t, review, metadata)
	}
}

func TestNativeControlApprovalReviewHiddenMetadataStillPinsExactRequest(t *testing.T) {
	request := generatedApproval("item/commandExecution/requestApproval", "approval", map[string]any{"startedAtMs": 123})
	c, server, state, _, _ := approvalFixture(t, request)
	server.mu.Lock()
	server.requests[0] = generatedApproval(request.Method, "approval", map[string]any{"startedAtMs": 124})
	server.mu.Unlock()
	changed, err := c.Inspect(context.Background(), controlTestThread)
	require.NoError(t, err)
	require.Equal(t, state.PendingApprovals[0].Detail, changed.PendingApprovals[0].Detail)
	require.NotEqual(t, state.PendingApprovals[0].PromptID, changed.PendingApprovals[0].PromptID)
	require.ErrorIs(t, c.AnswerApproval(context.Background(), approvalAnswer(state, "allow_once")), agent.ErrNativeControlStale)
	server.mu.Lock()
	defer server.mu.Unlock()
	require.Zero(t, server.mutations)
}
