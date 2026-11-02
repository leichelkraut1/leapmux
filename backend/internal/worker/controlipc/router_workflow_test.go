package controlipc_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// A CONFINED workflow agent's socket (its template set confine_agents)
// reaches its run's participant methods on its coordinator, and nothing else:
// no other worker method, no other computer, no hub.
func TestRouterWorkflowConfinement(t *testing.T) {
	t.Parallel()

	newRouter := func(local, coordinator string) (*controlipc.Router, *fakeLocalDispatcher, *fakeCrossWorker, *fakeHubClient) {
		d := &fakeLocalDispatcher{respPayload: []byte{}}
		cw := &fakeCrossWorker{resp: []byte{}}
		hub := &fakeHubClient{resp: []byte{}}
		return &controlipc.Router{
			WorkerID: local, UserID: userid.MustNew("u"), LocalDispatcher: d, CrossWorker: cw, Hub: hub,
			Confine: &controlipc.WorkflowConfinement{CoordinatorWorkerID: coordinator, Methods: service.WorkflowParticipantMethods},
		}, d, cw, hub
	}
	info := controlipc.TokenInfo{UserID: userid.MustNew("u"), WorkerID: "builder", TabID: "agent-1"}

	t.Run("allows a participant method to the coordinator across workers", func(t *testing.T) {
		t.Parallel()
		r, _, cw, _ := newRouter("builder", "lead")
		_, err := r.CallInner(t.Context(), info, "worker.WorkflowAgentReport", nil, "lead")
		require.NoError(t, err)
		assert.Equal(t, "lead", cw.callTarget)
		assert.Equal(t, "WorkflowAgentReport", cw.callMethod)
	})
	t.Run("allows a participant method locally when the coordinator is this worker", func(t *testing.T) {
		t.Parallel()
		r, d, _, _ := newRouter("lead", "lead")
		_, err := r.CallInner(t.Context(), info, "worker.WorkflowAgentDelegate", nil, "")
		require.NoError(t, err)
		assert.Equal(t, "WorkflowAgentDelegate", d.gotMethod)
	})
	for name, call := range map[string]struct{ method, target string }{
		"refuses opening an agent":               {"worker.OpenAgent", "lead"},
		"refuses messaging an arbitrary agent":   {"worker.EnqueueAgentInput", "lead"},
		"refuses deciding an approval":           {"worker.DecideWorkflowApproval", "lead"},
		"refuses a participant method elsewhere": {"worker.WorkflowAgentReport", "mac"},
		"refuses a participant method locally":   {"worker.WorkflowAgentReport", ""},
		"refuses the hub":                        {"hub.ListWorkers", ""},
		"refuses a file read on the coordinator": {"worker.ReadFile", "lead"},
		"refuses a terminal on another computer": {"worker.OpenTerminal", "mac"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, d, cw, hub := newRouter("builder", "lead")
			_, err := r.CallInner(t.Context(), info, call.method, nil, call.target)
			require.Error(t, err)
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			assert.Empty(t, d.gotMethod, "nothing reaches the local dispatcher")
			assert.Empty(t, cw.callMethod, "nothing reaches another worker")
			assert.Empty(t, hub.lastMethod, "nothing reaches the hub")
		})
	}
	t.Run("refuses streams too", func(t *testing.T) {
		t.Parallel()
		r, _, _, _ := newRouter("builder", "lead")
		err := r.StreamInner(t.Context(), info, "worker.WatchEvents", nil, "lead", "req-1", func(*leapmuxv1.StreamInnerEnvelope) error { return nil })
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
	t.Run("an unreadable binding confines to nothing", func(t *testing.T) {
		t.Parallel()
		r, _, _, _ := newRouter("builder", "")
		_, err := r.CallInner(t.Context(), info, "worker.WorkflowAgentContext", nil, "")
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
}

func TestEnvVarsCarryTheWorkflowCredential(t *testing.T) {
	t.Parallel()
	info := controlipc.TokenInfo{
		UserID: userid.MustNew("u"), WorkerID: "builder", TabID: "agent-1",
		Workflow: &leapmuxv1.WorkflowBinding{RunId: "run-1", SlotId: "slot-1", RoleKey: "builder", CoordinatorWorkerId: "lead", Token: "secret"},
	}
	envs := strings.Join(controlipc.EnvVars("unix:/sock", "tok", info), "\n")
	for _, want := range []string{
		"LEAPMUX_WORKFLOW_RUN_ID=run-1", "LEAPMUX_WORKFLOW_ROLE=builder",
		"LEAPMUX_WORKFLOW_COORDINATOR_WORKER_ID=lead", "LEAPMUX_WORKFLOW_TOKEN=secret",
	} {
		assert.Contains(t, envs, want)
	}
	plain := strings.Join(controlipc.EnvVars("unix:/sock", "tok", controlipc.TokenInfo{UserID: userid.MustNew("u")}), "\n")
	assert.NotContains(t, plain, "LEAPMUX_WORKFLOW_", "an ordinary agent carries no workflow credential")
}
