package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// An agent is the owner's agent: it can drive the owner's workflows. (That
// every workflow method also sits inside a cross-worker agent's grant is
// TestEveryRegisteredMethodIsWithinTheDelegationGrant's job.)
func TestWorkflowMethodsServeAnAgentCaller(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	owner := userid.MustNew("user-1")
	svc.SetRegisteredBy(owner)

	w := newTestWriter()
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(owner), &leapmuxv1.InnerRpcRequest{Method: "InstallExampleWorkflow"}, w)
	require.Empty(t, w.rejections())
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.InstallExampleWorkflowResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Equal(t, "Develop, audit and test on iOS", resp.GetTemplate().GetName())

	w = newTestWriter()
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(owner), &leapmuxv1.InnerRpcRequest{Method: "ListWorkflowLibrary"}, w)
	require.Len(t, w.responses, 1)
	var lib leapmuxv1.ListWorkflowLibraryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &lib))
	assert.Len(t, lib.GetTemplates(), 1)
	assert.False(t, lib.GetTemplates()[0].GetConfineAgents(), "the example leaves its agents unconfined")
}

// The run credential is how the engine knows which role is speaking, so a
// participant call without a valid one is refused.
func TestWorkflowParticipantMethodsNeedTheRunCredential(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	owner := userid.MustNew("user-1")
	svc.SetRegisteredBy(owner)

	payload, err := proto.Marshal(&leapmuxv1.WorkflowAgentDelegateRequest{
		Auth: &leapmuxv1.WorkflowAgentAuth{RunId: "run-1", Token: "forged"}, RoleKey: "builder", Title: "x", Instructions: "x",
	})
	require.NoError(t, err)
	w := newTestWriter()
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(owner), &leapmuxv1.InnerRpcRequest{Method: "WorkflowAgentDelegate", Payload: payload}, w)
	rejected := w.rejections()
	require.Len(t, rejected, 1)
	assert.Equal(t, codePermissionDenied, rejected[0].code)
}

func TestOpenAgentRefusesAnIncompleteWorkflowBinding(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	payload, err := proto.Marshal(&leapmuxv1.OpenAgentRequest{
		WorkerId: "w", WorkingDir: t.TempDir(), Workflow: &leapmuxv1.WorkflowBinding{RunId: "run-1"},
	})
	require.NoError(t, err)
	w := newTestWriter()
	d.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{Method: "OpenAgent", Payload: payload}, w)
	require.Len(t, w.errors, 1)
}

func TestWorkerCapabilitiesNameTheOS(t *testing.T) {
	t.Parallel()
	caps := workerCapabilities()
	assert.NotEmpty(t, caps)
	var hasOS bool
	for _, c := range caps {
		hasOS = hasOS || len(c) > 3 && c[:3] == "os:"
	}
	assert.True(t, hasOS)
}
