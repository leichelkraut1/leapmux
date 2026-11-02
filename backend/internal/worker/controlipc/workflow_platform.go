package controlipc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// systemInfoTTL is how long a computer's name and capabilities are reused.
// Capabilities change when software is installed, which is rare; asking on
// every pass would cost one cross-worker handshake per computer per pass.
const systemInfoTTL = 5 * time.Minute

// platformCallTimeout bounds every call the engine makes. The engine loop is
// sequential, so a computer that accepts a channel and then never answers
// would otherwise stall every run on this coordinator. A timed-out call is a
// transient failure: the next pass tries again.
const platformCallTimeout = 90 * time.Second

// WorkflowPlatform is the workflow engine's reach beyond its database. It
// calls through an UNCONFINED router scoped to the worker's registrant, the
// same path a `leapmux control` command from an ordinary agent takes: the
// local dispatcher for this computer, the cross-worker E2EE channel for
// another, and the hub (under the user's delegation) for the computer list
// and the workspace layout. The engine is the authority for confinement, so
// it is the one caller that holds this router.
type WorkflowPlatform struct {
	Factory *Factory
	// Owner is the worker's registrant, the only user whose runs it hosts.
	Owner func() userid.UserID

	originClient string
	once         sync.Once

	mu    sync.Mutex
	infos map[string]cachedSystemInfo
}

type cachedSystemInfo struct {
	name         string
	capabilities []string
	at           time.Time
}

var _ workflow.Platform = (*WorkflowPlatform)(nil)

func (p *WorkflowPlatform) router() (*Router, TokenInfo, error) {
	owner := p.Owner()
	if owner.IsZero() {
		return nil, TokenInfo{}, errors.New("the worker has no registered owner yet")
	}
	p.once.Do(func() { p.originClient = id.Generate() })
	return p.Factory.newRouter(owner), TokenInfo{UserID: owner, WorkerID: p.Factory.WorkerID}, nil
}

// call invokes one inner method and decodes its reply. A refusal that
// retrying cannot change comes back as a workflow.PermanentError.
func (p *WorkflowPlatform) call(ctx context.Context, targetWorkerID, method string, in, out proto.Message) error {
	r, info, err := p.router()
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(in)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, platformCallTimeout)
	defer cancel()
	resp, err := r.CallInner(ctx, info, method, payload, targetWorkerID)
	if err != nil {
		return classifyConnect(err)
	}
	if resp.GetIsError() {
		return classifyCode(codes.Code(resp.GetErrorCode()), errors.New(resp.GetErrorMessage()))
	}
	if out != nil && len(resp.GetPayload()) > 0 {
		return proto.Unmarshal(resp.GetPayload(), out)
	}
	return nil
}

func classifyConnect(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeNotFound,
		connect.CodePermissionDenied, connect.CodeAlreadyExists, connect.CodeUnimplemented:
		return &workflow.PermanentError{Err: err}
	default:
		return err
	}
}

func classifyCode(code codes.Code, err error) error {
	switch code {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied,
		codes.AlreadyExists, codes.Unimplemented:
		return &workflow.PermanentError{Err: err}
	default:
		return err
	}
}

func (p *WorkflowPlatform) Workers(ctx context.Context) ([]workflow.WorkerCandidate, error) {
	var workers []*leapmuxv1.Worker
	cursor := ""
	for range 20 {
		var resp leapmuxv1.ListWorkersResponse
		if err := p.call(ctx, "", "hub.ListWorkers", &leapmuxv1.ListWorkersRequest{Page: &leapmuxv1.PageRequest{Cursor: cursor}}, &resp); err != nil {
			return nil, err
		}
		workers = append(workers, resp.GetWorkers()...)
		if !resp.GetPage().GetHasMore() || resp.GetPage().GetNextCursor() == "" {
			break
		}
		cursor = resp.GetPage().GetNextCursor()
	}
	out := make([]workflow.WorkerCandidate, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		out[i] = workflow.WorkerCandidate{ID: w.GetId(), Online: w.GetOnline()}
		if !w.GetOnline() {
			if cached, ok := p.cachedInfo(w.GetId(), true); ok {
				out[i].Name = cached.name
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, ok := p.systemInfo(ctx, w.GetId())
			if ok {
				out[i].Name, out[i].Capabilities = info.name, info.capabilities
			}
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p *WorkflowPlatform) cachedInfo(workerID string, anyAge bool) (cachedSystemInfo, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, ok := p.infos[workerID]
	if !ok || (!anyAge && time.Since(info.at) > systemInfoTTL) {
		return cachedSystemInfo{}, false
	}
	return info, true
}

func (p *WorkflowPlatform) systemInfo(ctx context.Context, workerID string) (cachedSystemInfo, bool) {
	if info, ok := p.cachedInfo(workerID, false); ok {
		return info, true
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var resp leapmuxv1.GetWorkerSystemInfoResponse
	if err := p.call(callCtx, workerID, "worker.GetWorkerSystemInfo", &leapmuxv1.GetWorkerSystemInfoRequest{}, &resp); err != nil {
		// An online computer that does not answer has unknown capabilities,
		// which matches no requirement -- placement then waits for it.
		return cachedSystemInfo{}, false
	}
	info := cachedSystemInfo{name: resp.GetName(), capabilities: resp.GetCapabilities(), at: time.Now()}
	p.mu.Lock()
	if p.infos == nil {
		p.infos = map[string]cachedSystemInfo{}
	}
	p.infos[workerID] = info
	p.mu.Unlock()
	return info, true
}

func (p *WorkflowPlatform) OpenAgent(ctx context.Context, workerID string, req *leapmuxv1.OpenAgentRequest) (string, error) {
	var resp leapmuxv1.OpenAgentResponse
	if err := p.call(ctx, workerID, "worker.OpenAgent", req, &resp); err != nil {
		return "", err
	}
	if resp.GetAgent().GetId() == "" {
		return "", errors.New("the worker opened no agent")
	}
	return resp.GetAgent().GetId(), nil
}

// AddAgentTab places the agent last on the workspace's root tile, writing the
// same three tab registers `leapmux control tab open` writes. The HLC is an
// advisory hint; the hub assigns the canonical one on commit.
func (p *WorkflowPlatform) AddAgentTab(ctx context.Context, workspaceID, workerID, agentID string) error {
	if workspaceID == "" {
		return errors.New("the run has no workspace")
	}
	var state leapmuxv1.GetMaterializedResponse
	if err := p.call(ctx, "", "hub.GetMaterialized", &leapmuxv1.GetMaterializedRequest{WorkspaceIds: []string{workspaceID}}, &state); err != nil {
		return err
	}
	ws := state.GetState().GetWorkspaces()[workspaceID]
	tileID := ws.GetRootNodeId()
	if tileID == "" {
		return fmt.Errorf("workspace %s has no layout yet", workspaceID)
	}
	last := ""
	for _, t := range state.GetState().GetTabs() {
		if t.GetTabId() == agentID && t.GetTileId().GetValue() != "" && hlcZero(t.GetTombstoneAt()) {
			return nil // already placed by an earlier attempt
		}
		if hlcZero(t.GetTombstoneAt()) && t.GetTileId().GetValue() == tileID && t.GetPosition().GetValue() > last {
			last = t.GetPosition().GetValue()
		}
	}
	position := lexorank.First()
	if last != "" {
		position = lexorank.After(last)
	}
	hlc := &leapmuxv1.HLC{Physical: time.Now().UnixMilli(), ClientId: p.originClient}
	set := func(field func(*leapmuxv1.SetTabRegisterOp)) *leapmuxv1.CrdtOp {
		inner := &leapmuxv1.SetTabRegisterOp{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}
		field(inner)
		hlc = &leapmuxv1.HLC{Physical: hlc.GetPhysical(), Logical: hlc.GetLogical() + 1, ClientId: p.originClient}
		return &leapmuxv1.CrdtOp{
			OpId: id.Generate(), OriginClientId: p.originClient, ClientHlc: hlc,
			Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: inner},
		}
	}
	batch := &leapmuxv1.OpBatch{BatchId: id.Generate(), Ops: []*leapmuxv1.CrdtOp{
		set(func(o *leapmuxv1.SetTabRegisterOp) { o.Field = &leapmuxv1.SetTabRegisterOp_TileId{TileId: tileID} }),
		set(func(o *leapmuxv1.SetTabRegisterOp) {
			o.Field = &leapmuxv1.SetTabRegisterOp_Position{Position: position}
		}),
		set(func(o *leapmuxv1.SetTabRegisterOp) {
			o.Field = &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID}
		}),
	}}
	var resp leapmuxv1.SubmitOpsResponse
	if err := p.call(ctx, "", "hub.SubmitOps", &leapmuxv1.SubmitOpsRequest{Epoch: state.GetState().GetCurrentEpoch(), Batches: []*leapmuxv1.OpBatch{batch}}, &resp); err != nil {
		return err
	}
	if len(resp.GetResults()) != 1 {
		return fmt.Errorf("the hub answered %d results for one batch", len(resp.GetResults()))
	}
	if rej := resp.GetResults()[0].GetRejected(); rej != nil {
		return fmt.Errorf("the hub rejected the tab: %s", rej.GetReason().String())
	}
	return nil
}

func hlcZero(h *leapmuxv1.HLC) bool {
	return h == nil || (h.GetPhysical() == 0 && h.GetLogical() == 0 && h.GetClientId() == "")
}

func (p *WorkflowPlatform) EnqueueInput(ctx context.Context, workerID, agentID, inputID, text string) error {
	return p.call(ctx, workerID, "worker.EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: agentID, InputId: inputID, Text: text, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	}, nil)
}

func (p *WorkflowPlatform) AgentStates(ctx context.Context, workerID string, agentIDs []string) (map[string]workflow.AgentState, error) {
	var resp leapmuxv1.ListAgentsResponse
	if err := p.call(ctx, workerID, "worker.ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: agentIDs}, &resp); err != nil {
		return nil, err
	}
	out := map[string]workflow.AgentState{}
	for _, v := range resp.GetVerdicts() {
		if v.GetStatus() == leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_ABSENT {
			out[v.GetTabId()] = workflow.AgentGone
		}
	}
	for _, a := range resp.GetAgents() {
		state := workflow.AgentAlive
		if a.GetClosedAt() != "" || a.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
			state = workflow.AgentGone
		}
		out[a.GetId()] = state
	}
	return out, nil
}

func (p *WorkflowPlatform) InterruptAgent(ctx context.Context, workerID, agentID string) error {
	return p.call(ctx, workerID, "worker.InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: agentID, Reason: "workflow"}, nil)
}

func (p *WorkflowPlatform) FindAgents(ctx context.Context, workerID, runID string) (map[string]string, error) {
	var resp leapmuxv1.FindWorkflowAgentsResponse
	if err := p.call(ctx, workerID, "worker.FindWorkflowAgents", &leapmuxv1.FindWorkflowAgentsRequest{RunId: runID}, &resp); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range resp.GetAgents() {
		out[e.GetSlotId()] = e.GetAgentId()
	}
	return out, nil
}
