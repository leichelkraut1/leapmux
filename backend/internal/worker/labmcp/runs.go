package labmcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/tradinglab"
)

// RunControl is what the run tools need from the workflow engine
// (*workflow.Engine): reading runs, the library, starting a run and
// answering a step's approval. Nothing here reaches an order: a live order
// is approved only in the person's browser.
type RunControl interface {
	RunLister
	GetRun(ctx context.Context, userID, runID string) (*leapmuxv1.WorkflowRunDetail, error)
	Library(ctx context.Context, userID string) (*leapmuxv1.ListWorkflowLibraryResponse, error)
	CreateRun(ctx context.Context, userID string, req *leapmuxv1.CreateWorkflowRunRequest) (*leapmuxv1.CreateWorkflowRunResponse, error)
	DecideApproval(ctx context.Context, userID string, req *leapmuxv1.DecideWorkflowApprovalRequest) (*leapmuxv1.WorkflowRunDetail, error)
}

func word(prefix, name string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

// startResearchRun starts a run like the latest trading run: its workspace,
// project directory and the computer each role ran on, with the library's
// current version of its template.
func (s *Server) startResearchRun(ctx context.Context, owner string, args map[string]any) (any, error) {
	ctl, ok := s.cfg.Runs.(RunControl)
	if !ok {
		return nil, badArg("this worker cannot start runs")
	}
	focus, err := stringArg(args, "objective")
	if err != nil {
		return nil, err
	}
	runs, err := s.tradingRuns(ctx, owner)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, badArg("start the first research run in LeapMux; later ones can start from here")
	}
	last, err := ctl.GetRun(ctx, owner, runs[0].GetRun().GetId())
	if err != nil {
		return nil, err
	}
	lib, err := ctl.Library(ctx, owner)
	if err != nil {
		return nil, err
	}
	template := last.GetRun().GetTemplate()
	for _, t := range lib.GetTemplates() {
		if t.GetName() == template.GetName() && t.GetVersion() >= template.GetVersion() {
			template = t
		}
	}
	var overrides []*leapmuxv1.WorkflowRoleOverride
	for _, a := range last.GetRoles() {
		if a.GetWorkerId() != "" && slices.ContainsFunc(template.GetRoles(), func(r *leapmuxv1.WorkflowRole) bool { return r.GetKey() == a.GetRoleKey() }) {
			overrides = append(overrides, &leapmuxv1.WorkflowRoleOverride{RoleKey: a.GetRoleKey(), Placement: &leapmuxv1.WorkerPlacement{
				Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER, WorkerId: a.GetWorkerId(),
			}})
		}
	}
	objective := strings.TrimSpace(focus)
	if objective == "" {
		objective = last.GetRun().GetObjective()
	}
	created, err := ctl.CreateRun(ctx, owner, &leapmuxv1.CreateWorkflowRunRequest{
		Template: proto.Clone(template).(*leapmuxv1.WorkflowTemplate), Profiles: lib.GetProfiles(), Overrides: overrides,
		Objective: objective, WorkingDir: last.GetRun().GetWorkingDir(), WorkspaceId: last.GetRun().GetWorkspaceId(),
	})
	if err != nil {
		return nil, fmt.Errorf("start the run: %w", err)
	}
	run := created.GetRun()
	return map[string]any{
		"run_id": run.GetId(), "name": run.GetName(), "status": word("WORKFLOW_RUN_STATUS_", run.GetStatus().String()),
		"template_version": run.GetTemplateVersion(), "objective": run.GetObjective(),
		"next": "Follow it with get_run_progress. A step that runs out of attempts asks for approval, which decide_step_approval answers.",
	}, nil
}

// runProgress is a run's steps and the approvals it waits on.
func (s *Server) runProgress(ctx context.Context, owner string, args map[string]any) (any, error) {
	ctl, ok := s.cfg.Runs.(RunControl)
	if !ok {
		return nil, badArg("this worker cannot read runs")
	}
	runID, err := s.tradingRunArg(ctx, owner, args)
	if err != nil {
		return nil, err
	}
	d, err := ctl.GetRun(ctx, owner, runID)
	if err != nil {
		return nil, err
	}
	var tasks, approvals []map[string]any
	for _, t := range d.GetTasks() {
		tasks = append(tasks, map[string]any{
			"title": t.GetTitle(), "step": t.GetStepKey(), "status": word("WORKFLOW_TASK_STATUS_", t.GetStatus().String()),
			"detail": t.GetStatusDetail(), "attempt": t.GetAttempt(),
		})
	}
	for _, a := range d.GetApprovals() {
		if a.GetStatus() != leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING {
			continue
		}
		approvals = append(approvals, map[string]any{"approval_id": a.GetId(), "title": a.GetTitle(), "details": a.GetDetails()})
	}
	return map[string]any{
		"run_id": runID, "status": word("WORKFLOW_RUN_STATUS_", d.GetRun().GetStatus().String()), "detail": d.GetRun().GetStatusDetail(),
		"tasks": tasks, "pending_approvals": approvals,
	}, nil
}

// decideStepApproval answers a run step's approval, such as another attempt
// at a step that failed. It cannot approve an order: those are not step
// approvals, and the worker accepts them only from the person's browser.
func (s *Server) decideStepApproval(ctx context.Context, owner string, args map[string]any) (any, error) {
	ctl, ok := s.cfg.Runs.(RunControl)
	if !ok {
		return nil, badArg("this worker cannot answer approvals")
	}
	runID, err := s.tradingRunArg(ctx, owner, args)
	if err != nil {
		return nil, err
	}
	approvalID, err := stringArg(args, "approval_id")
	if err != nil || approvalID == "" {
		return nil, badArg("approval_id is required")
	}
	approve, ok := args["approve"].(bool)
	if !ok {
		return nil, badArg("approve must be true or false")
	}
	note, err := stringArg(args, "note")
	if err != nil {
		return nil, err
	}
	d, err := ctl.DecideApproval(ctx, owner, &leapmuxv1.DecideWorkflowApprovalRequest{RunId: runID, ApprovalId: approvalID, Approve: approve, Note: note})
	if err != nil {
		return nil, err
	}
	return map[string]any{"run_id": runID, "status": word("WORKFLOW_RUN_STATUS_", d.GetRun().GetStatus().String()), "detail": d.GetRun().GetStatusDetail()}, nil
}

// tradingRunArg is run_id, which must name one of the user's trading runs.
func (s *Server) tradingRunArg(ctx context.Context, owner string, args map[string]any) (string, error) {
	runID, err := stringArg(args, "run_id")
	if err != nil || runID == "" {
		return "", badArg("run_id is required")
	}
	runs, err := s.tradingRuns(ctx, owner)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(runs, func(r *leapmuxv1.WorkflowRunSummary) bool { return r.GetRun().GetId() == runID }) {
		return "", tradinglab.ErrNotFound
	}
	return runID, nil
}

// actionTool is a tool that changes something: it starts or advances a run.
func actionTool(name, title, desc string, props map[string]any, required ...string) map[string]any {
	t := readOnlyTool(name, title, desc, props, required...)
	t["annotations"] = map[string]any{"title": title, "readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}
	return t
}

func init() {
	tools["start_research_run"] = tool{
		def: actionTool("start_research_run", "Start a research run",
			"Start a blind trading research run like the latest one: same workspace, project folder and computers, with the library's current template. It researches and records shadow books; it never places an order. Returns the run id.",
			map[string]any{"objective": map[string]any{"type": "string", "description": "What this run should focus on (default: the latest run's objective)."}}),
		run: (*Server).startResearchRun,
	}
	tools["get_run_progress"] = tool{
		def: readOnlyTool("get_run_progress", "Get run progress",
			"A research run's status, each step's status and detail, and the approvals it is waiting for (approval_id, title, details).",
			map[string]any{"run_id": map[string]any{"type": "string"}}, "run_id"),
		run: (*Server).runProgress,
	}
	tools["decide_step_approval"] = tool{
		def: actionTool("decide_step_approval", "Answer a step approval",
			"Approve or reject a research run's pending step approval, such as another attempt at a step that ran out of attempts. Live orders are not step approvals and can only be approved in LeapMux in the browser.",
			map[string]any{
				"run_id":      map[string]any{"type": "string"},
				"approval_id": map[string]any{"type": "string", "description": "From get_run_progress."},
				"approve":     map[string]any{"type": "boolean"},
				"note":        map[string]any{"type": "string"},
			}, "run_id", "approval_id", "approve"),
		run: (*Server).decideStepApproval,
	}
	toolList = sortedTools()
}
