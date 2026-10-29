package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// Workflow methods use the ordinary agent scopes, so whoever may drive the
// owner's agents -- the browser, the CLI, or one of the agents themselves --
// may drive their workflows too:
//
//   - The owner's methods (library, launch, intervene, approve): reads under
//     agent:read, changes under agent:write.
//   - A participant's (WorkflowAgent*) methods, under the same scopes plus the
//     run credential, which tells the engine which role is speaking.
//   - FindWorkflowAgents is the coordinator asking a hosting worker, under
//     agent:read.
//
// Every group is owner-only, like every agent handler.
func registerWorkflowHandlers(r registrar, svc *Service) {
	const read, write = leapmuxv1.Scope_SCOPE_AGENT_READ, leapmuxv1.Scope_SCOPE_AGENT_WRITE
	engine := svc.Workflows

	registerOwnerGuarded(r, "ListWorkflowLibrary", read, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.ListWorkflowLibraryRequest, sender channel.ResponseWriter) {
			resp, err := engine.Library(ctx, caller.UserID.String())
			replyWorkflow(sender, resp, err)
		})
	registerOwnerGuarded(r, "SaveAgentProfile", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.SaveAgentProfileRequest, sender channel.ResponseWriter) {
			p, err := engine.SaveProfile(ctx, caller.UserID.String(), req.GetProfile())
			replyWorkflow(sender, &leapmuxv1.SaveAgentProfileResponse{Profile: p}, err)
		})
	registerOwnerGuarded(r, "DeleteAgentProfile", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.DeleteAgentProfileRequest, sender channel.ResponseWriter) {
			err := engine.DeleteProfile(ctx, caller.UserID.String(), req.GetProfileId())
			replyWorkflow(sender, &leapmuxv1.DeleteAgentProfileResponse{}, err)
		})
	registerOwnerGuarded(r, "SaveWorkflowTemplate", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.SaveWorkflowTemplateRequest, sender channel.ResponseWriter) {
			t, err := engine.SaveTemplate(ctx, caller.UserID.String(), req.GetTemplate())
			replyWorkflow(sender, &leapmuxv1.SaveWorkflowTemplateResponse{Template: t}, err)
		})
	registerOwnerGuarded(r, "DeleteWorkflowTemplate", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.DeleteWorkflowTemplateRequest, sender channel.ResponseWriter) {
			err := engine.DeleteTemplate(ctx, caller.UserID.String(), req.GetTemplateId())
			replyWorkflow(sender, &leapmuxv1.DeleteWorkflowTemplateResponse{}, err)
		})
	registerOwnerGuarded(r, "ExportWorkflowTemplate", read, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.ExportWorkflowTemplateRequest, sender channel.ResponseWriter) {
			resp, err := engine.Export(ctx, caller.UserID.String(), req.GetTemplateId(), req.GetFormat())
			replyWorkflow(sender, resp, err)
		})
	registerOwnerGuarded(r, "ImportWorkflowTemplate", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.ImportWorkflowTemplateRequest, sender channel.ResponseWriter) {
			resp, err := engine.Import(ctx, caller.UserID.String(), req.GetDocument())
			replyWorkflow(sender, resp, err)
		})
	registerOwnerGuarded(r, "InstallExampleWorkflow", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.InstallExampleWorkflowRequest, sender channel.ResponseWriter) {
			resp, err := engine.InstallExample(ctx, caller.UserID.String())
			var out *leapmuxv1.InstallExampleWorkflowResponse
			if resp != nil {
				out = &leapmuxv1.InstallExampleWorkflowResponse{Template: resp.GetTemplate(), Profiles: resp.GetProfiles()}
			}
			replyWorkflow(sender, out, err)
		})
	registerOwnerGuarded(r, "CreateWorkflowRun", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.CreateWorkflowRunRequest, sender channel.ResponseWriter) {
			resp, err := engine.CreateRun(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, resp, err)
		})
	registerOwnerGuarded(r, "AttachWorkflowLead", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.AttachWorkflowLeadRequest, sender channel.ResponseWriter) {
			detail, err := engine.AttachLead(ctx, caller.UserID.String(), req.GetRunId(), req.GetAgentId())
			replyWorkflow(sender, &leapmuxv1.AttachWorkflowLeadResponse{Detail: detail}, err)
		})
	registerOwnerGuarded(r, "ListWorkflowRuns", read, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, _ *leapmuxv1.ListWorkflowRunsRequest, sender channel.ResponseWriter) {
			runs, err := engine.ListRuns(ctx, caller.UserID.String())
			replyWorkflow(sender, &leapmuxv1.ListWorkflowRunsResponse{Runs: runs}, err)
		})
	registerOwnerGuarded(r, "GetWorkflowRun", read, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.GetWorkflowRunRequest, sender channel.ResponseWriter) {
			detail, err := engine.GetRun(ctx, caller.UserID.String(), req.GetRunId())
			replyWorkflow(sender, &leapmuxv1.GetWorkflowRunResponse{Detail: detail}, err)
		})
	registerOwnerGuarded(r, "ControlWorkflowRun", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.ControlWorkflowRunRequest, sender channel.ResponseWriter) {
			detail, err := engine.ControlRun(ctx, caller.UserID.String(), req.GetRunId(), req.GetAction())
			replyWorkflow(sender, &leapmuxv1.ControlWorkflowRunResponse{Detail: detail}, err)
		})
	registerOwnerGuarded(r, "ControlWorkflowTask", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.ControlWorkflowTaskRequest, sender channel.ResponseWriter) {
			detail, err := engine.ControlTask(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.ControlWorkflowTaskResponse{Detail: detail}, err)
		})
	registerOwnerGuarded(r, "UpdateWorkflowRole", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.UpdateWorkflowRoleRequest, sender channel.ResponseWriter) {
			detail, err := engine.UpdateRole(ctx, caller.UserID.String(), req.GetRunId(), req.GetOverride())
			replyWorkflow(sender, &leapmuxv1.UpdateWorkflowRoleResponse{Detail: detail}, err)
		})
	registerOwnerGuarded(r, "DecideWorkflowApproval", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.DecideWorkflowApprovalRequest, sender channel.ResponseWriter) {
			detail, err := engine.DecideApproval(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.DecideWorkflowApprovalResponse{Detail: detail}, err)
		})
	registerOwnerGuarded(r, "SendWorkflowAgentMessage", write, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.SendWorkflowAgentMessageRequest, sender channel.ResponseWriter) {
			err := engine.SendMessage(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.SendWorkflowAgentMessageResponse{}, err)
		})

	registerOwnerGuarded(r, "FindWorkflowAgents", leapmuxv1.Scope_SCOPE_AGENT_READ, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, req *leapmuxv1.FindWorkflowAgentsRequest, sender channel.ResponseWriter) {
			entries, err := engine.Store().BindingsForRun(ctx, req.GetRunId())
			replyWorkflow(sender, &leapmuxv1.FindWorkflowAgentsResponse{Agents: entries}, err)
		})

	registerOwnerGuarded(r, "WorkflowAgentContext", leapmuxv1.Scope_SCOPE_AGENT_READ, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentContextRequest, sender channel.ResponseWriter) {
			resp, err := engine.AgentContext(ctx, caller.UserID.String(), req.GetAuth())
			replyWorkflow(sender, resp, err)
		})
	registerOwnerGuarded(r, "WorkflowAgentDelegate", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentDelegateRequest, sender channel.ResponseWriter) {
			t, err := engine.Delegate(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.WorkflowAgentDelegateResponse{Task: t}, err)
		})
	registerOwnerGuarded(r, "WorkflowAgentSend", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentSendRequest, sender channel.ResponseWriter) {
			err := engine.Send(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.WorkflowAgentSendResponse{}, err)
		})
	registerOwnerGuarded(r, "WorkflowAgentReport", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentReportRequest, sender channel.ResponseWriter) {
			t, err := engine.Report(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.WorkflowAgentReportResponse{Task: t}, err)
		})
	registerOwnerGuarded(r, "WorkflowAgentProgress", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentProgressRequest, sender channel.ResponseWriter) {
			err := engine.Progress(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.WorkflowAgentProgressResponse{}, err)
		})
	registerOwnerGuarded(r, "WorkflowAgentReview", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentReviewRequest, sender channel.ResponseWriter) {
			t, err := engine.Review(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.WorkflowAgentReviewResponse{Task: t}, err)
		})
	registerOwnerGuarded(r, "WorkflowAgentRequestApproval", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentRequestApprovalRequest, sender channel.ResponseWriter) {
			a, err := engine.RequestApproval(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.WorkflowAgentRequestApprovalResponse{Approval: a}, err)
		})
	registerOwnerGuarded(r, "WorkflowAgentComplete", leapmuxv1.Scope_SCOPE_AGENT_WRITE, dispatchPlain,
		func(ctx context.Context, caller channel.Caller, req *leapmuxv1.WorkflowAgentCompleteRequest, sender channel.ResponseWriter) {
			run, err := engine.Complete(ctx, caller.UserID.String(), req)
			replyWorkflow(sender, &leapmuxv1.WorkflowAgentCompleteResponse{Run: run}, err)
		})
}

// WorkflowParticipantMethods are the inner methods a CONFINED workflow
// agent's control socket may call, on its coordinator only (a template's
// confine_agents). An unconfined workflow agent reaches everything.
var WorkflowParticipantMethods = []string{
	"WorkflowAgentContext",
	"WorkflowAgentDelegate",
	"WorkflowAgentSend",
	"WorkflowAgentReport",
	"WorkflowAgentProgress",
	"WorkflowAgentReview",
	"WorkflowAgentRequestApproval",
	"WorkflowAgentComplete",
}

// replyWorkflow answers with resp, or maps an engine error to its code.
func replyWorkflow(sender channel.ResponseWriter, resp proto.Message, err error) {
	switch {
	case err == nil:
		sendProtoResponse(sender, resp)
	case errors.Is(err, workflow.ErrInvalid):
		sendInvalidArgument(sender, err.Error())
	case errors.Is(err, workflow.ErrNotFound):
		sendNotFoundError(sender, "not found")
	case errors.Is(err, workflow.ErrConflict):
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, workflow.ErrDenied):
		sendPermissionDenied(sender, err.Error())
	default:
		sendInternalError(sender, "workflow: "+err.Error())
	}
}

// validateWorkflowBinding refuses an OpenAgent whose binding is incomplete. A
// binding grants nothing -- at most it narrows the agent's socket -- so any
// caller allowed to open an agent may open it bound.
func validateWorkflowBinding(b *leapmuxv1.WorkflowBinding) error {
	if b == nil {
		return nil
	}
	if b.GetRunId() == "" || b.GetSlotId() == "" || b.GetRoleKey() == "" || b.GetCoordinatorWorkerId() == "" || b.GetToken() == "" {
		return errors.New("a workflow binding needs run_id, slot_id, role_key, coordinator_worker_id and token")
	}
	return nil
}

// workflowBindingFor reads a local agent's binding for a relaunch, so a
// resumed workflow agent keeps its credential (and its confinement, when its
// template asked for one).
func (svc *Service) workflowBindingFor(agentID string) *leapmuxv1.WorkflowBinding {
	if svc.Workflows == nil {
		return nil
	}
	b, err := svc.Workflows.Store().Binding(bgCtx(), agentID)
	if err != nil {
		// The relaunch proceeds as an ordinary agent; the next relaunch
		// reads the binding again.
		slog.Warn("read workflow binding for relaunch", "agent_id", agentID, "error", err)
		return nil
	}
	return b
}

// workerCapabilities is what this computer offers workflow placement: its OS
// and architecture, the tools found on PATH, and whatever the operator lists
// in LEAPMUX_WORKER_CAPABILITIES. Detected once per process.
var workerCapabilities = sync.OnceValue(func() []string {
	caps := []string{"os:" + runtime.GOOS, "arch:" + runtime.GOARCH}
	tools := map[string]string{"git": "git", "docker": "docker", "node": "node", "go": "go", "python3": "python"}
	if runtime.GOOS == "darwin" {
		tools["xcodebuild"] = "xcode"
	}
	for bin, capability := range tools {
		if _, err := exec.LookPath(bin); err == nil {
			caps = append(caps, capability)
		}
	}
	for _, c := range strings.Split(os.Getenv("LEAPMUX_WORKER_CAPABILITIES"), ",") {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			caps = append(caps, c)
		}
	}
	slices.Sort(caps)
	return slices.Compact(caps)
})
