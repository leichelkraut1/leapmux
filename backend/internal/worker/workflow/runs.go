package workflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// --- User: launch ---

// CreateRun records a PENDING run that this worker coordinates, with the
// Lead's slot reserved here. The caller opens the Lead with the returned
// binding and then calls AttachLead; until then the run does nothing.
func (e *Engine) CreateRun(ctx context.Context, userID string, req *leapmuxv1.CreateWorkflowRunRequest) (*leapmuxv1.CreateWorkflowRunResponse, error) {
	template := proto.Clone(req.GetTemplate()).(*leapmuxv1.WorkflowTemplate)
	if err := NormalizeTemplate(template); err != nil {
		return nil, err
	}
	roles, err := ResolveRoles(template, req.GetProfiles(), req.GetOverrides())
	if err != nil {
		return nil, err
	}
	objective := strings.TrimSpace(req.GetObjective())
	if objective == "" {
		return nil, invalidf("a run needs an objective")
	}
	if len(objective) > maxObjectiveBytes {
		return nil, invalidf("the objective is longer than %d bytes", maxObjectiveBytes)
	}
	workingDir := strings.TrimSpace(req.GetWorkingDir())
	if workingDir == "" {
		return nil, invalidf("a run needs a project directory")
	}
	if strings.TrimSpace(req.GetWorkspaceId()) == "" {
		return nil, invalidf("a run needs a workspace for its agents' tabs")
	}
	lead := leadRole(roles)
	if err := e.leadMayRunHere(lead.GetAgent().GetPlacement()); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		name = template.GetName()
	}
	if name, err = checkName("a run", name); err != nil {
		return nil, err
	}

	now := e.nowMs()
	run := &leapmuxv1.WorkflowRun{
		Id: e.newID(), Name: name, TemplateId: template.GetId(), TemplateVersion: template.GetVersion(), Template: template,
		Status: leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PENDING, StatusDetail: "Waiting for the Lead agent to start",
		Objective: objective, WorkingDir: workingDir, BaseBranch: strings.TrimSpace(req.GetBaseBranch()),
		WorkspaceId: req.GetWorkspaceId(), CoordinatorWorkerId: e.workerID, CreatedAtMs: now, UpdatedAtMs: now,
	}
	token := e.newToken()
	leadSlot := &leapmuxv1.WorkflowRunAgent{
		SlotId: e.newID(), RunId: run.GetId(), RoleKey: lead.GetKey(), WorkerId: e.workerID,
		Status: leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING, StatusDetail: "Opening the Lead", CreatedAtMs: now,
	}
	run.LeadSlotId = leadSlot.GetSlotId()
	err = e.mutate(ctx, func(q querier) error {
		if err := insertRun(ctx, q, userID, run); err != nil {
			return err
		}
		for _, role := range roles {
			workerID := ""
			if p := role.GetAgent().GetPlacement(); p.GetStrategy() == leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER {
				workerID = p.GetWorkerId()
			}
			if role.GetIsLead() {
				workerID = e.workerID
			}
			if err := putRole(ctx, q, run.GetId(), &leapmuxv1.WorkflowRoleAssignment{RoleKey: role.GetKey(), Role: role, WorkerId: workerID}); err != nil {
				return err
			}
		}
		if err := insertSlot(ctx, q, leadSlot, token, now); err != nil {
			return err
		}
		rs := &runState{userID: userID, run: run}
		return e.event(ctx, q, rs, "", leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_RUN, "user",
			"Run created from template %q (version %d)", template.GetName(), template.GetVersion())
	})
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.CreateWorkflowRunResponse{
		Run: run,
		LeadBinding: &leapmuxv1.WorkflowBinding{
			RunId: run.GetId(), SlotId: leadSlot.GetSlotId(), RoleKey: lead.GetKey(), CoordinatorWorkerId: e.workerID, Token: token,
			Confine: template.GetConfineAgents(),
		},
		LeadRole: lead,
	}, nil
}

// leadMayRunHere refuses a Lead whose placement excludes this computer. A run
// is coordinated where its Lead runs, so the frontend creates it there; this
// is the check that a pinned Lead is never quietly coordinated elsewhere.
func (e *Engine) leadMayRunHere(p *leapmuxv1.WorkerPlacement) error {
	switch p.GetStrategy() {
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER:
		if p.GetWorkerId() != e.workerID {
			return conflictf("the Lead is pinned to computer %s; launch the run from there", p.GetWorkerId())
		}
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY:
		self := WorkerCandidate{ID: e.workerID, Online: true, Capabilities: e.capabilities()}
		if err := AllowsWorker(p, self); err != nil {
			return conflictf("the Lead cannot run on this computer: %v", err)
		}
	default:
		if ids := p.GetAllowedWorkerIds(); len(ids) > 0 && !slices.Contains(ids, e.workerID) {
			return conflictf("the Lead may not run on this computer")
		}
	}
	return nil
}

// AttachLead binds the opened Lead agent to its run, starts the run, plans
// the template's steps as tasks, and briefs the Lead. The agent must be the
// local agent opened with the run's Lead binding, which is what keeps a
// caller from attaching an arbitrary agent as a Lead.
func (e *Engine) AttachLead(ctx context.Context, userID, runID, agentID string) (*leapmuxv1.WorkflowRunDetail, error) {
	binding, err := e.store.Binding(ctx, agentID)
	if err != nil {
		return nil, err
	}
	var detail *leapmuxv1.WorkflowRunDetail
	err = e.mutate(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, userID, runID)
		if err != nil {
			return err
		}
		if binding == nil || binding.GetRunId() != runID || binding.GetSlotId() != rs.run.GetLeadSlotId() {
			return deniedf("agent %s was not opened as this run's Lead", agentID)
		}
		if rs.run.GetLeadAgentId() == agentID {
			detail, err = rs.detail(ctx, q)
			return err
		}
		if rs.run.GetStatus() != leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PENDING {
			return conflictf("the run already started")
		}
		slot := rs.slot(rs.run.GetLeadSlotId())
		slot.AgentId, slot.Status, slot.StatusDetail = agentID, leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE, ""
		if err := e.saveSlot(ctx, q, slot); err != nil {
			return err
		}
		rs.run.LeadAgentId = agentID
		rs.run.StartedAtMs = e.nowMs()
		if err := e.planSteps(ctx, q, rs); err != nil {
			return err
		}
		if err := e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, "", "user"); err != nil {
			return err
		}
		if err := e.send(ctx, q, rs, slot.GetSlotId(), "", leadBriefing(rs.run, rs.roles, rs.tasks, e.workerName)); err != nil {
			return err
		}
		detail, err = rs.detail(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	e.Wake()
	return detail, nil
}

// planSteps turns the template's default process into tasks: PENDING until
// the Lead releases them, or QUEUED for an auto-start step.
func (e *Engine) planSteps(ctx context.Context, q querier, rs *runState) error {
	template := rs.run.GetTemplate()
	ids := map[string]string{}
	for _, s := range template.GetSteps() {
		ids[s.GetKey()] = e.newID()
	}
	for _, s := range template.GetSteps() {
		status := leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_PENDING
		detail := "Planned; waiting for the Lead to release it"
		if s.GetAutoStart() {
			status, detail = leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, ""
		}
		var deps []string
		for _, d := range s.GetDependsOn() {
			deps = append(deps, ids[d])
		}
		t := &leapmuxv1.WorkflowTask{
			Id: ids[s.GetKey()], RunId: rs.run.GetId(), StepKey: s.GetKey(), RoleKey: s.GetRoleKey(), Title: s.GetTitle(),
			Instructions: s.GetInstructions(), DependsOn: deps, Status: status, StatusDetail: detail, Approval: s.GetApproval(),
			MaxAttempts: taskMaxAttempts(template, s.GetMaxAttempts()), CreatedBy: "template",
			CreatedAtMs: e.nowMs(), UpdatedAtMs: e.nowMs(),
		}
		if err := insertTask(ctx, q, t); err != nil {
			return err
		}
		rs.tasks = append(rs.tasks, t)
	}
	return nil
}

// --- User: observe ---

func (e *Engine) ListRuns(ctx context.Context, userID string) ([]*leapmuxv1.WorkflowRunSummary, error) {
	runs, err := listRuns(ctx, e.store.db, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*leapmuxv1.WorkflowRunSummary, 0, len(runs))
	for _, run := range runs {
		tasks, err := listTasks(ctx, e.store.db, run.GetId())
		if err != nil {
			return nil, err
		}
		approvals, err := listApprovals(ctx, e.store.db, run.GetId())
		if err != nil {
			return nil, err
		}
		sum := &leapmuxv1.WorkflowRunSummary{Run: run, TasksTotal: uint32(len(tasks))}
		for _, t := range tasks {
			switch {
			case t.GetStatus() == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED:
				sum.TasksSucceeded++
			case t.GetStatus() == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED:
				sum.TasksBlocked++
			case taskInFlight(t.GetStatus()) || t.GetStatus() == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUBMITTED:
				sum.TasksActive++
			}
		}
		for _, a := range approvals {
			if a.GetStatus() == leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING {
				sum.ApprovalsPending++
			}
		}
		// The list shows no template; the detail view does.
		run.Template = &leapmuxv1.WorkflowTemplate{Id: run.GetTemplate().GetId(), Name: run.GetTemplate().GetName()}
		out = append(out, sum)
	}
	return out, nil
}

func (e *Engine) GetRun(ctx context.Context, userID, runID string) (*leapmuxv1.WorkflowRunDetail, error) {
	var detail *leapmuxv1.WorkflowRunDetail
	err := e.store.inTx(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, userID, runID)
		if err != nil {
			return err
		}
		detail, err = rs.detail(ctx, q)
		return err
	})
	return detail, err
}

// --- User: intervene ---

// userChange runs fn on a loaded run, then returns the run's fresh detail.
func (e *Engine) userChange(ctx context.Context, userID, runID string, fn func(q querier, rs *runState) error) (*leapmuxv1.WorkflowRunDetail, error) {
	var detail *leapmuxv1.WorkflowRunDetail
	err := e.mutate(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, userID, runID)
		if err != nil {
			return err
		}
		if err := fn(q, rs); err != nil {
			return err
		}
		if rs, err = loadRun(ctx, q, userID, runID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil // deleted
			}
			return err
		}
		detail, err = rs.detail(ctx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	e.Wake()
	return detail, nil
}

func (e *Engine) ControlRun(ctx context.Context, userID, runID string, action leapmuxv1.WorkflowRunAction) (*leapmuxv1.WorkflowRunDetail, error) {
	var interrupt []*leapmuxv1.WorkflowRunAgent
	detail, err := e.userChange(ctx, userID, runID, func(q querier, rs *runState) error {
		status := rs.run.GetStatus()
		switch action {
		case leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_PAUSE:
			if !runDispatches(status) {
				return conflictf("only a running or waiting run can be paused")
			}
			return e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PAUSED,
				"Paused by the user: no new tasks start and no messages are sent", "user")
		case leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_RESUME:
			if status != leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PAUSED && status != leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_BLOCKED {
				return conflictf("only a paused or blocked run can be resumed")
			}
			if lead := rs.slot(rs.run.GetLeadSlotId()); lead == nil || !slotHolds(lead.GetStatus()) {
				return conflictf("the Lead agent is gone, so the run cannot continue; cancel it")
			}
			if err := e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, "", "user"); err != nil {
				return err
			}
			return e.settleRunStatus(ctx, q, rs)
		case leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_CANCEL:
			if runFinal(status) {
				return conflictf("the run already finished")
			}
			for _, t := range rs.tasks {
				if !taskFinal(t.GetStatus()) {
					if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED, "user", "The run was cancelled"); err != nil {
						return err
					}
				}
			}
			if err := e.cancelPendingApprovals(ctx, q, rs, ""); err != nil {
				return err
			}
			if err := e.dropOutbox(ctx, q, rs, "", "run cancelled"); err != nil {
				return err
			}
			interrupt = rs.slots
			return e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_CANCELLED, "Cancelled by the user", "user")
		case leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_RECONCILE:
			return nil
		case leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_DELETE:
			if !runFinal(status) && status != leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PENDING {
				return conflictf("cancel the run before deleting it")
			}
			return deleteRun(ctx, q, rs.run.GetId())
		default:
			return invalidf("unknown run action %v", action)
		}
	})
	if err != nil {
		return nil, err
	}
	if len(interrupt) > 0 {
		e.interruptAll(ctx, interrupt)
	}
	if action == leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_RECONCILE {
		e.reconcileNow(ctx, userID, runID)
		return e.GetRun(ctx, userID, runID)
	}
	return detail, nil
}

func (e *Engine) reconcileNow(ctx context.Context, userID, runID string) {
	platform := e.currentPlatform()
	if platform == nil {
		return
	}
	workers, err := platform.Workers(ctx)
	if err != nil {
		return
	}
	e.rememberNames(workers)
	e.reconcile(ctx, platform, runRef{runID: runID, userID: userID}, workers)
	e.Wake()
}

func (e *Engine) cancelPendingApprovals(ctx context.Context, q querier, rs *runState, taskID string) error {
	for _, a := range rs.approvals {
		if a.GetStatus() != leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING || (taskID != "" && a.GetTaskId() != taskID) {
			continue
		}
		a.Status, a.DecidedAtMs = leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_CANCELLED, e.nowMs()
		if err := updateApproval(ctx, q, a); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) ControlTask(ctx context.Context, userID string, req *leapmuxv1.ControlWorkflowTaskRequest) (*leapmuxv1.WorkflowRunDetail, error) {
	var interrupt []*leapmuxv1.WorkflowRunAgent
	detail, err := e.userChange(ctx, userID, req.GetRunId(), func(q querier, rs *runState) error {
		if runFinal(rs.run.GetStatus()) {
			return conflictf("the run already finished")
		}
		t := rs.task(req.GetTaskId())
		if t == nil {
			return ErrNotFound
		}
		switch req.GetAction() {
		case leapmuxv1.WorkflowTaskAction_WORKFLOW_TASK_ACTION_RETRY:
			switch t.GetStatus() {
			case leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_FAILED,
				leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED:
			default:
				return conflictf("only a blocked, failed or cancelled task can be retried")
			}
			if t.GetAttempt() >= t.GetMaxAttempts() {
				t.MaxAttempts = t.GetAttempt() + 1
			}
			t.CompletedAtMs = 0
			if err := e.cancelPendingApprovals(ctx, q, rs, t.GetId()); err != nil {
				return err
			}
			if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, "user", "Retry requested by the user"); err != nil {
				return err
			}
			return e.notifyLead(ctx, q, rs, "The user re-queued task `%s` (%s).", t.GetId(), t.GetTitle())
		case leapmuxv1.WorkflowTaskAction_WORKFLOW_TASK_ACTION_CANCEL:
			if taskFinal(t.GetStatus()) {
				return conflictf("the task already finished")
			}
			if slot := rs.slot(t.GetSlotId()); slot != nil && taskInFlight(t.GetStatus()) {
				interrupt = append(interrupt, slot)
				if err := e.send(ctx, q, rs, slot.GetSlotId(), "", leadNote("The user cancelled task `%s` (%s). Stop working on it.", t.GetId(), t.GetTitle())); err != nil {
					return err
				}
			}
			if err := e.cancelPendingApprovals(ctx, q, rs, t.GetId()); err != nil {
				return err
			}
			if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED, "user", "Cancelled by the user"); err != nil {
				return err
			}
			return e.notifyLead(ctx, q, rs, "The user cancelled task `%s` (%s).", t.GetId(), t.GetTitle())
		case leapmuxv1.WorkflowTaskAction_WORKFLOW_TASK_ACTION_REASSIGN:
			return e.reassign(ctx, q, rs, t, req.GetWorkerId(), &interrupt)
		default:
			return invalidf("unknown task action %v", req.GetAction())
		}
	})
	if err != nil {
		return nil, err
	}
	if len(interrupt) > 0 {
		e.interruptAll(ctx, interrupt)
	}
	return detail, nil
}

// reassign pins a task to another computer and queues it for a fresh agent
// there. The user's choice is the authority for leaving a pinned role's
// computer, but an allow-list or a capability requirement still holds.
func (e *Engine) reassign(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, workerID string, interrupt *[]*leapmuxv1.WorkflowRunAgent) error {
	if taskFinal(t.GetStatus()) && t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_FAILED && t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED {
		return conflictf("a succeeded task cannot be reassigned")
	}
	if workerID == "" {
		return invalidf("reassigning needs a computer")
	}
	platform := e.currentPlatform()
	if platform == nil {
		return conflictf("the engine is not connected yet")
	}
	workers, err := platform.Workers(ctx)
	if err != nil {
		return conflictf("cannot list computers: %v", err)
	}
	var target *WorkerCandidate
	for i := range workers {
		if workers[i].ID == workerID {
			target = &workers[i]
		}
	}
	if target == nil {
		return invalidf("computer %s is not registered to this account", workerID)
	}
	assignment := rs.role(t.GetRoleKey())
	if err := AllowsWorker(assignment.GetRole().GetAgent().GetPlacement(), *target); err != nil {
		return invalidf("%v", err)
	}
	if slot := rs.slot(t.GetSlotId()); slot != nil && taskInFlight(t.GetStatus()) {
		*interrupt = append(*interrupt, slot)
		if err := e.send(ctx, q, rs, slot.GetSlotId(), "", leadNote("Task `%s` (%s) was reassigned to another computer. Stop working on it.", t.GetId(), t.GetTitle())); err != nil {
			return err
		}
	}
	if err := e.releaseSlot(ctx, q, rs, t); err != nil {
		return err
	}
	t.WorkerId, t.SlotId, t.CompletedAtMs = workerID, "", 0
	if t.GetAttempt() >= t.GetMaxAttempts() {
		t.MaxAttempts = t.GetAttempt() + 1
	}
	if err := e.cancelPendingApprovals(ctx, q, rs, t.GetId()); err != nil {
		return err
	}
	if err := e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_PLACEMENT, "user",
		"Reassigned %q to %s", t.GetTitle(), target.label()); err != nil {
		return err
	}
	if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, "user", "Reassigned to "+target.label()); err != nil {
		return err
	}
	return e.notifyLead(ctx, q, rs, "The user reassigned task `%s` (%s) to %s.", t.GetId(), t.GetTitle(), target.label())
}

// UpdateRole changes a role's configuration for agents opened from now on.
// An agent already running keeps the settings it was opened with.
func (e *Engine) UpdateRole(ctx context.Context, userID, runID string, o *leapmuxv1.WorkflowRoleOverride) (*leapmuxv1.WorkflowRunDetail, error) {
	return e.userChange(ctx, userID, runID, func(q querier, rs *runState) error {
		if runFinal(rs.run.GetStatus()) {
			return conflictf("the run already finished")
		}
		a := rs.role(o.GetRoleKey())
		if a == nil {
			return ErrNotFound
		}
		if a.GetRole().GetIsLead() && o.GetPlacement() != nil {
			return conflictf("the Lead runs on the coordinating computer and cannot move")
		}
		agent := mergeAgent(a.GetRole().GetAgent(), &leapmuxv1.AgentProfile{
			AgentProvider: o.GetAgentProvider(), Options: o.GetOptions(), Placement: o.GetPlacement(), WorkingDir: strings.TrimSpace(o.GetWorkingDir()),
		})
		if err := NormalizePlacement(agent.GetPlacement()); err != nil {
			return err
		}
		agent.Options = normalizeOptions(agent.GetOptions())
		a.Role.Agent = agent
		if p := agent.GetPlacement(); p.GetStrategy() == leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER {
			a.WorkerId = p.GetWorkerId()
		}
		if err := putRole(ctx, q, runID, a); err != nil {
			return err
		}
		return e.event(ctx, q, rs, "", leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_USER, "user",
			"Changed the %s role: %s, %s", a.GetRole().GetName(), roleSummary(a.GetRole()), placementSummary(a, e.workerName))
	})
}

// DecideApproval applies a human decision. Only the user reaches this: the
// scope it is registered under is outside every delegated grant.
func (e *Engine) DecideApproval(ctx context.Context, userID string, req *leapmuxv1.DecideWorkflowApprovalRequest) (*leapmuxv1.WorkflowRunDetail, error) {
	return e.userChange(ctx, userID, req.GetRunId(), func(q querier, rs *runState) error {
		a := rs.approval(req.GetApprovalId())
		if a == nil {
			return ErrNotFound
		}
		if a.GetStatus() != leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING {
			return conflictf("the request was already decided")
		}
		note := strings.TrimSpace(req.GetNote())
		a.Status, a.DecisionNote, a.DecidedAtMs = leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_REJECTED, note, e.nowMs()
		verdict := "rejected"
		if req.GetApprove() {
			a.Status, verdict = leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_APPROVED, "approved"
		}
		if err := updateApproval(ctx, q, a); err != nil {
			return err
		}
		msg := fmt.Sprintf("The user %s %q", verdict, a.GetTitle())
		if note != "" {
			msg += ": " + note
		}
		if err := e.event(ctx, q, rs, a.GetTaskId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_APPROVAL, "user", "%s", msg); err != nil {
			return err
		}
		if err := e.applyDecision(ctx, q, rs, a, req.GetApprove(), note); err != nil {
			return err
		}
		if err := e.notifyLead(ctx, q, rs, "%s (approval `%s`).", msg, a.GetId()); err != nil {
			return err
		}
		return e.settleRunStatus(ctx, q, rs)
	})
}

func (e *Engine) applyDecision(ctx context.Context, q querier, rs *runState, a *leapmuxv1.WorkflowApproval, approved bool, note string) error {
	t := rs.task(a.GetTaskId())
	switch a.GetKind() {
	case leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_TASK_START:
		if t == nil || t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_AWAITING_APPROVAL {
			return nil
		}
		if approved {
			return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, "user", "Start approved")
		}
		return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED, "user", "Start rejected by the user")
	case leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_TASK_COMPLETION:
		if t == nil || t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_AWAITING_APPROVAL {
			return nil
		}
		if approved {
			return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, "user", "Result approved")
		}
		return e.retryOrEscalate(ctx, q, rs, t, "user", "The user rejected the result: "+note)
	case leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_ESCALATION:
		if t == nil || t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED {
			return nil
		}
		if approved {
			t.MaxAttempts = t.GetAttempt() + 1
			return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, "user", "One more attempt granted")
		}
		return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_FAILED, "user", "The user declined another attempt")
	case leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_RUN_COMPLETION:
		if rs.run.GetStatus() != leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_WAITING {
			return nil
		}
		if approved {
			return e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED, "Approved by the user", "user")
		}
		rs.run.Summary = ""
		return e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, "", "user")
	case leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_OPERATION:
		// Nothing to apply: the Lead learns the decision and acts on it.
		return nil
	default:
		return nil
	}
}

// SendMessage records the user's own instruction to a participant.
func (e *Engine) SendMessage(ctx context.Context, userID string, req *leapmuxv1.SendWorkflowAgentMessageRequest) error {
	text := strings.TrimSpace(req.GetText())
	if text == "" {
		return invalidf("the message is empty")
	}
	_, err := e.userChange(ctx, userID, req.GetRunId(), func(q querier, rs *runState) error {
		slot := rs.slot(req.GetSlotId())
		if slot == nil || !slotHolds(slot.GetStatus()) {
			return conflictf("that agent is not active in the run")
		}
		if err := e.send(ctx, q, rs, slot.GetSlotId(), "", notePrefix+" Message from the user:\n\n"+text); err != nil {
			return err
		}
		return e.event(ctx, q, rs, slot.GetCurrentTaskId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_MESSAGE, "user",
			"The user wrote to the %s agent", slot.GetRoleKey())
	})
	return err
}

// --- Participants ---

// participant authenticates a workflow agent by its slot credential. The
// credential names exactly one slot of one run, so it reaches nothing else.
func (e *Engine) participant(ctx context.Context, q querier, userID string, auth *leapmuxv1.WorkflowAgentAuth) (*runState, *leapmuxv1.WorkflowRunAgent, error) {
	if auth.GetToken() == "" || auth.GetRunId() == "" {
		return nil, nil, deniedf("missing workflow credential (LEAPMUX_WORKFLOW_TOKEN)")
	}
	slot, err := slotByToken(ctx, q, auth.GetToken())
	if err != nil || slot.GetRunId() != auth.GetRunId() {
		return nil, nil, deniedf("the workflow credential is not valid for this run")
	}
	rs, err := loadRun(ctx, q, userID, slot.GetRunId())
	if err != nil {
		return nil, nil, deniedf("the workflow credential is not valid for this run")
	}
	slot = rs.slot(slot.GetSlotId())
	if !slotHolds(slot.GetStatus()) {
		return nil, nil, deniedf("this agent is no longer part of the run")
	}
	return rs, slot, nil
}

func requireLead(rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
	if !rs.isLead(slot) {
		return deniedf("only the Lead may do this")
	}
	return nil
}

func requireLive(rs *runState) error {
	if runFinal(rs.run.GetStatus()) {
		return conflictf("the run already finished")
	}
	if rs.run.GetStatus() == leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PENDING {
		return conflictf("the run has not started")
	}
	return nil
}

func (e *Engine) AgentContext(ctx context.Context, userID string, auth *leapmuxv1.WorkflowAgentAuth) (*leapmuxv1.WorkflowAgentContextResponse, error) {
	var resp *leapmuxv1.WorkflowAgentContextResponse
	err := e.store.inTx(ctx, func(q querier) error {
		rs, slot, err := e.participant(ctx, q, userID, auth)
		if err != nil {
			return err
		}
		resp = &leapmuxv1.WorkflowAgentContextResponse{
			Run: rs.run, SlotId: slot.GetSlotId(), RoleKey: slot.GetRoleKey(), IsLead: rs.isLead(slot),
			Roles: rs.roles, Agents: rs.slots, Tasks: rs.tasks, Approvals: rs.approvals,
		}
		if !resp.GetIsLead() {
			// An assignee sees its own tasks, not the whole board.
			var mine []*leapmuxv1.WorkflowTask
			for _, t := range rs.tasks {
				if t.GetSlotId() == slot.GetSlotId() {
					mine = append(mine, t)
				}
			}
			resp.Tasks, resp.Approvals = mine, nil
		}
		return nil
	})
	return resp, err
}

// participantChange runs fn for an authenticated participant of a live run.
func (e *Engine) participantChange(ctx context.Context, userID string, auth *leapmuxv1.WorkflowAgentAuth, fn func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error) error {
	err := e.mutate(ctx, func(q querier) error {
		rs, slot, err := e.participant(ctx, q, userID, auth)
		if err != nil {
			return err
		}
		if err := requireLive(rs); err != nil {
			return err
		}
		if err := fn(q, rs, slot); err != nil {
			return err
		}
		return e.settleRunStatus(ctx, q, rs)
	})
	if err == nil {
		e.Wake()
	}
	return err
}

func (e *Engine) Delegate(ctx context.Context, userID string, req *leapmuxv1.WorkflowAgentDelegateRequest) (*leapmuxv1.WorkflowTask, error) {
	var out *leapmuxv1.WorkflowTask
	err := e.participantChange(ctx, userID, req.GetAuth(), func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
		if err := requireLead(rs, slot); err != nil {
			return err
		}
		extra := strings.TrimSpace(req.GetInstructions())
		if err := checkText("instructions", extra); err != nil {
			return err
		}
		if req.GetTaskId() != "" {
			t := rs.task(req.GetTaskId())
			if t == nil {
				return ErrNotFound
			}
			if t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_PENDING {
				return conflictf("task %s was already released", t.GetId())
			}
			if extra != "" {
				t.Instructions = strings.TrimSpace(t.GetInstructions() + "\n\n" + extra)
			}
			if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, "lead", "Released by the Lead"); err != nil {
				return err
			}
			out = t
			return nil
		}
		a := rs.role(req.GetRoleKey())
		if a == nil {
			keys := make([]string, 0, len(rs.roles))
			for _, r := range rs.roles {
				if !r.GetRole().GetIsLead() {
					keys = append(keys, r.GetRoleKey())
				}
			}
			return invalidf("unknown role %q (roles: %s)", req.GetRoleKey(), strings.Join(keys, ", "))
		}
		if a.GetRole().GetIsLead() {
			return invalidf("the Lead cannot delegate to itself")
		}
		title, err := checkName("a task", req.GetTitle())
		if err != nil {
			return err
		}
		if extra == "" {
			return invalidf("a task needs instructions")
		}
		deps := normalizeList(req.GetDependsOn(), false)
		for _, d := range deps {
			if rs.task(d) == nil {
				return invalidf("dependency %s is not a task of this run", d)
			}
		}
		gate := req.GetApproval()
		if gate == leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_UNSPECIFIED {
			gate = leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_NONE
		}
		t := &leapmuxv1.WorkflowTask{
			Id: e.newID(), RunId: rs.run.GetId(), RoleKey: a.GetRoleKey(), Title: title, Instructions: extra, DependsOn: deps,
			Status: leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, Approval: gate,
			MaxAttempts: taskMaxAttempts(rs.run.GetTemplate(), req.GetMaxAttempts()), CreatedBy: "lead",
			CreatedAtMs: e.nowMs(), UpdatedAtMs: e.nowMs(),
		}
		if err := insertTask(ctx, q, t); err != nil {
			return err
		}
		rs.tasks = append(rs.tasks, t)
		out = t
		return e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_TASK, "lead",
			"The Lead delegated %q to %s", title, a.GetRole().GetName())
	})
	return out, err
}

func (e *Engine) Send(ctx context.Context, userID string, req *leapmuxv1.WorkflowAgentSendRequest) error {
	return e.participantChange(ctx, userID, req.GetAuth(), func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
		if err := requireLead(rs, slot); err != nil {
			return err
		}
		text := strings.TrimSpace(req.GetText())
		if text == "" {
			return invalidf("the message is empty")
		}
		t := rs.task(req.GetTaskId())
		if t == nil {
			return ErrNotFound
		}
		target := rs.slot(t.GetSlotId())
		if target == nil || !slotHolds(target.GetStatus()) {
			return conflictf("task %s has no active agent; delegate a new task instead", t.GetId())
		}
		if err := e.send(ctx, q, rs, target.GetSlotId(), "", fmt.Sprintf("%s Follow-up from the Lead on task `%s`:\n\n%s", notePrefix, t.GetId(), text)); err != nil {
			return err
		}
		return e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_MESSAGE, "lead", "The Lead sent a follow-up on %q", t.GetTitle())
	})
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// validateResult checks a report against what its role requires. This is the
// engine refusing to take "done" on an agent's word.
func validateResult(role *leapmuxv1.WorkflowRole, r *leapmuxv1.WorkflowTaskResult) error {
	if r == nil {
		return invalidf("a report needs a result")
	}
	switch r.GetOutcome() {
	case leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED, leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_FAILED,
		leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_BLOCKED:
	default:
		return invalidf("a report needs an outcome: completed, failed or blocked")
	}
	r.Summary = strings.TrimSpace(r.GetSummary())
	if r.GetSummary() == "" {
		return invalidf("a report needs a summary")
	}
	if err := checkText("the summary", r.GetSummary()); err != nil {
		return err
	}
	r.GitCommit = strings.ToLower(strings.TrimSpace(r.GetGitCommit()))
	if r.GetGitCommit() != "" && !commitPattern.MatchString(r.GetGitCommit()) {
		return invalidf("commit %q is not a hexadecimal SHA", r.GetGitCommit())
	}
	if r.GetTestsPassed() && !r.GetTestsRan() {
		r.TestsRan = true
	}
	if r.GetOutcome() != leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED {
		return nil
	}
	var missing []string
	for _, out := range role.GetRequiredOutputs() {
		switch out {
		case OutputCommit:
			if r.GetGitCommit() == "" || strings.TrimSpace(r.GetGitBranch()) == "" {
				missing = append(missing, "the branch and commit SHA (--branch, --commit)")
			}
		case OutputTests:
			if !r.GetTestsRan() || strings.TrimSpace(r.GetTestSummary()) == "" {
				missing = append(missing, "test results (--tests-passed or --tests-failed, --test-summary)")
			} else if !r.GetTestsPassed() {
				return invalidf("the tests failed, so the task is not completed; report --outcome failed")
			}
		case OutputArtifacts:
			if len(r.GetArtifacts()) == 0 {
				missing = append(missing, "at least one artifact (--artifact)")
			}
		}
	}
	if len(missing) > 0 {
		return invalidf("a completed %s report must include %s", role.GetName(), strings.Join(missing, "; "))
	}
	return nil
}

func (e *Engine) Report(ctx context.Context, userID string, req *leapmuxv1.WorkflowAgentReportRequest) (*leapmuxv1.WorkflowTask, error) {
	var out *leapmuxv1.WorkflowTask
	err := e.participantChange(ctx, userID, req.GetAuth(), func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
		t := rs.task(req.GetTaskId())
		if t == nil {
			return ErrNotFound
		}
		if t.GetSlotId() != slot.GetSlotId() {
			return deniedf("task %s is not assigned to you", t.GetId())
		}
		if !taskInFlight(t.GetStatus()) {
			return conflictf("task %s is %s; only a task you are working on can be reported", t.GetId(), taskWord(t.GetStatus()))
		}
		assignment := rs.role(t.GetRoleKey())
		result := proto.Clone(req.GetResult()).(*leapmuxv1.WorkflowTaskResult)
		if err := validateResult(assignment.GetRole(), result); err != nil {
			return err
		}
		result.ReportedBySlotId, result.ReportedAtMs = slot.GetSlotId(), e.nowMs()
		t.Result = result
		if err := e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_RESULT, slot.GetRoleKey(),
			"%s reported %s: %s", assignment.GetRole().GetName(), outcomeWord(result.GetOutcome()), firstLine(result.GetSummary())); err != nil {
			return err
		}
		out = t
		completed := result.GetOutcome() == leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED
		if assignment.GetRole().GetReviewRequired() {
			if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUBMITTED, slot.GetRoleKey(), "Waiting for the Lead's review"); err != nil {
				return err
			}
			return e.notifyLead(ctx, q, rs, "Task `%s` (%s) was reported by the %s and needs your review.\n\n%s\nAccept with `leapmux control workflow review --task %s --accept`, or send it back with `--reject --feedback <text>`.",
				t.GetId(), t.GetTitle(), assignment.GetRole().GetName(), resultSummary(result), t.GetId())
		}
		if completed {
			if err := e.acceptResult(ctx, q, rs, t, "engine"); err != nil {
				return err
			}
			return e.notifyLead(ctx, q, rs, "Task `%s` (%s) completed.\n\n%s", t.GetId(), t.GetTitle(), resultSummary(result))
		}
		if err := e.notifyLead(ctx, q, rs, "Task `%s` (%s) reported %s.\n\n%s", t.GetId(), t.GetTitle(), outcomeWord(result.GetOutcome()), resultSummary(result)); err != nil {
			return err
		}
		return e.retryOrEscalate(ctx, q, rs, t, "engine", result.GetSummary())
	})
	return out, err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 160 {
		line = line[:160] + "..."
	}
	return line
}

func (e *Engine) Progress(ctx context.Context, userID string, req *leapmuxv1.WorkflowAgentProgressRequest) error {
	return e.participantChange(ctx, userID, req.GetAuth(), func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
		note := strings.TrimSpace(req.GetNote())
		if note == "" {
			return invalidf("the note is empty")
		}
		t := rs.task(req.GetTaskId())
		if t == nil {
			return ErrNotFound
		}
		if t.GetSlotId() != slot.GetSlotId() && !rs.isLead(slot) {
			return deniedf("task %s is not assigned to you", t.GetId())
		}
		if taskInFlight(t.GetStatus()) {
			t.StatusDetail = firstLine(note)
			if err := e.saveTask(ctx, q, t); err != nil {
				return err
			}
		}
		return e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_TASK, slot.GetRoleKey(), "Progress: %s", firstLine(note))
	})
}

func (e *Engine) Review(ctx context.Context, userID string, req *leapmuxv1.WorkflowAgentReviewRequest) (*leapmuxv1.WorkflowTask, error) {
	var out *leapmuxv1.WorkflowTask
	err := e.participantChange(ctx, userID, req.GetAuth(), func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
		if err := requireLead(rs, slot); err != nil {
			return err
		}
		t := rs.task(req.GetTaskId())
		if t == nil {
			return ErrNotFound
		}
		if t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUBMITTED {
			return conflictf("task %s is %s, not waiting for review", t.GetId(), taskWord(t.GetStatus()))
		}
		out = t
		feedback := strings.TrimSpace(req.GetFeedback())
		if req.GetAccept() {
			if err := e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_REVIEW, "lead", "The Lead accepted %q", t.GetTitle()); err != nil {
				return err
			}
			if t.GetResult().GetOutcome() != leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED {
				return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_FAILED, "lead", "The Lead accepted the failure")
			}
			return e.acceptResult(ctx, q, rs, t, "lead")
		}
		if feedback == "" {
			return invalidf("sending a result back needs --feedback")
		}
		if err := e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_REVIEW, "lead", "The Lead sent %q back: %s", t.GetTitle(), firstLine(feedback)); err != nil {
			return err
		}
		return e.retryOrEscalate(ctx, q, rs, t, "lead", feedback)
	})
	return out, err
}

func (e *Engine) RequestApproval(ctx context.Context, userID string, req *leapmuxv1.WorkflowAgentRequestApprovalRequest) (*leapmuxv1.WorkflowApproval, error) {
	var out *leapmuxv1.WorkflowApproval
	err := e.participantChange(ctx, userID, req.GetAuth(), func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
		if err := requireLead(rs, slot); err != nil {
			return err
		}
		title, err := checkName("an approval request", req.GetTitle())
		if err != nil {
			return err
		}
		if err := checkText("details", req.GetDetails()); err != nil {
			return err
		}
		if req.GetTaskId() != "" && rs.task(req.GetTaskId()) == nil {
			return ErrNotFound
		}
		out, err = e.requestApproval(ctx, q, rs, leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_OPERATION, req.GetTaskId(),
			title, strings.TrimSpace(req.GetDetails()), "lead")
		return err
	})
	return out, err
}

func (e *Engine) Complete(ctx context.Context, userID string, req *leapmuxv1.WorkflowAgentCompleteRequest) (*leapmuxv1.WorkflowRun, error) {
	var out *leapmuxv1.WorkflowRun
	err := e.participantChange(ctx, userID, req.GetAuth(), func(q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent) error {
		if err := requireLead(rs, slot); err != nil {
			return err
		}
		summary := strings.TrimSpace(req.GetSummary())
		if summary == "" {
			return invalidf("completing the run needs a summary")
		}
		if err := checkText("the summary", summary); err != nil {
			return err
		}
		for _, a := range rs.approvals {
			if a.GetStatus() == leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING {
				return conflictf("approval %q is still pending", a.GetTitle())
			}
		}
		out = rs.run
		rs.run.Summary = summary
		if req.GetFailed() {
			return e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_FAILED, "The Lead reported failure", "lead")
		}
		var open []string
		for _, t := range rs.tasks {
			if taskOutstanding(t.GetStatus()) {
				open = append(open, fmt.Sprintf("%s (%s, %s)", t.GetId(), t.GetTitle(), taskWord(t.GetStatus())))
			}
		}
		if len(open) > 0 {
			return conflictf("tasks are still open: %s", strings.Join(open, "; "))
		}
		if rs.run.GetTemplate().GetRequireFinalApproval() {
			if err := e.saveRun(ctx, q, rs); err != nil {
				return err
			}
			_, err := e.requestApproval(ctx, q, rs, leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_RUN_COMPLETION, "",
				"Complete the run?", summary, "lead")
			return err
		}
		return e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED, "", "lead")
	})
	return out, err
}
