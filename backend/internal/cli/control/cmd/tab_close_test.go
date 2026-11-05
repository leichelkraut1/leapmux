package cmd

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

type closeLifetimeTrace struct {
	mu      sync.Mutex
	methods []string
}

func (trace *closeLifetimeTrace) record(method string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.methods = append(trace.methods, method)
}

func (trace *closeLifetimeTrace) snapshot() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.methods...)
}

type closeLifetimeHub struct {
	*recordingHub
	trace *closeLifetimeTrace
}

func (hub *closeLifetimeHub) CallInner(ctx context.Context, userID userid.UserID, method string, payload []byte) ([]byte, error) {
	if method != "SubmitOps" {
		return hub.recordingHub.CallInner(ctx, userID, method, payload)
	}
	hub.trace.record(method)
	return proto.Marshal(&leapmuxv1.SubmitOpsResponse{Results: []*leapmuxv1.BatchResult{{
		Outcome: &leapmuxv1.BatchResult_Committed{Committed: &leapmuxv1.BatchCommitted{}},
	}}})
}

type closeLifetimeDispatcher struct {
	trace *closeLifetimeTrace
}

func (dispatcher *closeLifetimeDispatcher) DispatchWith(_ context.Context, _ channel.Caller, request *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	dispatcher.trace.record(request.GetMethod())
	var response proto.Message
	switch request.GetMethod() {
	case "InspectLastTabClose":
		response = &leapmuxv1.InspectLastTabCloseResponse{}
	case "InspectTerminalProcesses":
		response = &leapmuxv1.InspectTerminalProcessesResponse{}
	case "CloseTerminal":
		response = &leapmuxv1.CloseTerminalResponse{}
	default:
		_ = writer.SendError(int32(codes.Unimplemented), "unexpected method: "+request.GetMethod())
		return
	}
	payload, err := proto.Marshal(response)
	if err != nil {
		_ = writer.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = writer.SendResponse(&leapmuxv1.InnerRpcResponse{Payload: payload})
}

// Closing the supplied scope closes a real transport. Escaping its workerCall
// therefore fails the next RPC, as the remote E2EE channel did in production.
func TestRunTabClose_KeepsTransportOpenThroughWorkerTeardown(t *testing.T) {
	clearRemoteEnv(t)
	trace := &closeLifetimeTrace{}
	hub := &closeLifetimeHub{
		recordingHub: &recordingHub{
			listWorkers: []string{"worker-A"},
			materialized: newStateBuilder().
				workspace("ws-1", "root-1").
				leafNode("root-1", "", "m").
				tab("terminal-2", "root-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
				st,
			locateTab: &leapmuxv1.WorkspaceTab{
				TabId: "terminal-2", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL,
				TileId: "root-1", WorkerId: "worker-A", WorkspaceId: "ws-1",
			},
		},
		trace: trace,
	}
	url := locallistentest.UniqueListenURL(t, "leapmux-close-lifetime")
	token := controlipc.MintToken()
	server, err := controlipc.Listen(controlipc.Options{
		SocketURL: url,
		Token:     token,
		TokenInfo: controlipc.TokenInfo{
			UserID: userid.MustNew("u-spawn"), WorkerID: "worker-A",
			TabID: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
		Router: &controlipc.Router{
			WorkerID: "worker-A", UserID: userid.MustNew("u-spawn"),
			Hub: hub, LocalDispatcher: &closeLifetimeDispatcher{trace: trace},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv("LEAPMUX_CONTROL_SOCK", url)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", token)

	scopeCalls := 0
	scopeClosed := false
	withChannel := func(ctx context.Context, client *control.Client, workerID string, body func(workerCall) error) error {
		scopeCalls++
		require.NoError(t, ctx.Err())
		require.Equal(t, "worker-A", workerID)
		defer func() {
			require.NoError(t, server.Close())
			scopeClosed = true
		}()
		return body(workerCall{c: client, workerID: workerID})
	}
	output := withCapturedStdout(t, func() {
		require.NoError(t, runTabClose(fakeCmdCtx{}, []string{
			"--tab-id", "terminal-2", "--tab-type", "terminal",
			"--workspace-id", "ws-1", "--worker-id", "worker-A",
			"--worktree", "keep",
		}, withChannel))
	})
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(output, &envelope))
	assert.Equal(t, true, envelope.Data["tombstoned"])
	assert.Equal(t, "terminal-2", envelope.Data["tab_id"])
	assert.NotContains(t, envelope.Data, "worker_close_error")
	assert.Equal(t, []string{"InspectLastTabClose", "InspectTerminalProcesses", "SubmitOps", "CloseTerminal"}, trace.snapshot())
	assert.Equal(t, 1, scopeCalls)
	assert.True(t, scopeClosed, "the transport must be released after every close step completes")
}
