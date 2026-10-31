package workflow

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
)

// Errors an operation returns for the caller to map onto an RPC code.
var (
	// ErrConflict is a request the run's current state does not admit.
	ErrConflict = errors.New("not allowed in the current state")
	// ErrDenied is a participant acting outside its run or role.
	ErrDenied = errors.New("not permitted")
)

func conflictf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

func deniedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDenied, fmt.Sprintf(format, args...))
}

// AgentState is what a hosting worker says about one agent.
type AgentState int

const (
	// AgentAlive covers a running agent and one still starting.
	AgentAlive AgentState = iota + 1
	// AgentGone is closed, failed to start, or absent from its worker.
	AgentGone
)

// PermanentError wraps a platform failure that retrying cannot fix (an
// invalid working directory, a branch that already exists). Every other
// platform error is treated as transient: the computer may come back.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Platform is everything the engine asks of LeapMux outside its own database:
// the user's computers, and agents on them. The production implementation
// routes through the worker's control router, so a call to another computer
// travels the cross-worker E2EE channel under the user's delegation.
type Platform interface {
	// Workers lists the user's computers, with capabilities for online ones.
	Workers(ctx context.Context) ([]WorkerCandidate, error)
	// OpenAgent opens an agent on workerID and returns its id.
	OpenAgent(ctx context.Context, workerID string, req *leapmuxv1.OpenAgentRequest) (string, error)
	// AddAgentTab shows an opened agent as a tab in workspaceID.
	AddAgentTab(ctx context.Context, workspaceID, workerID, agentID string) error
	// EnqueueInput delivers one message to an agent. inputID makes a resend
	// of the same message a no-op on the hosting worker.
	EnqueueInput(ctx context.Context, workerID, agentID, inputID, text string) error
	// AgentStates reports each requested agent's state on workerID.
	AgentStates(ctx context.Context, workerID string, agentIDs []string) (map[string]AgentState, error)
	// InterruptAgent stops an agent's current turn.
	InterruptAgent(ctx context.Context, workerID, agentID string) error
	// FindAgents lists the agents workerID opened for runID, by slot id.
	FindAgents(ctx context.Context, workerID, runID string) (map[string]string, error)
}

// Config configures an Engine.
type Config struct {
	DB       *sql.DB
	WorkerID string
	Clock    quartz.Clock
	Logger   *slog.Logger
	// Capabilities reports this computer's own capabilities, which decide
	// whether a capability-placed Lead may run here.
	Capabilities func() []string
	// ReconcileInterval is how often every active run re-checks its agents
	// and computers. Zero means 30 seconds.
	ReconcileInterval time.Duration
}

// Engine is the durable workflow runtime of one coordinator.
//
// All state lives in the worker database. mu serializes every transition, so
// two calls never interleave a read-modify-write of one run; network calls to
// other computers run OUTSIDE it, between a recorded intent and its recorded
// outcome, which is what makes each of them safe to repeat after a restart.
type Engine struct {
	store        *Store
	workerID     string
	clock        quartz.Clock
	log          *slog.Logger
	capabilities func() []string
	interval     time.Duration

	mu sync.Mutex

	platformMu sync.RWMutex
	platform   Platform

	wake chan struct{}

	// opening holds the slots this process is opening right now, so a pass
	// that overlaps a slow open does not open the same slot twice.
	opening sync.Map

	namesMu sync.RWMutex
	names   map[string]string

	newID    func() string
	newToken func() string
}

// New builds an engine. It does nothing until Run or Pass is called.
func New(cfg Config) *Engine {
	clock := cfg.Clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	interval := cfg.ReconcileInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	caps := cfg.Capabilities
	if caps == nil {
		caps = func() []string { return nil }
	}
	return &Engine{
		store:        NewStore(cfg.DB),
		workerID:     cfg.WorkerID,
		clock:        clock,
		log:          log.With("component", "workflow"),
		capabilities: caps,
		interval:     interval,
		wake:         make(chan struct{}, 1),
		names:        map[string]string{},
		newID:        id.Generate,
		newToken:     mintToken,
	}
}

func mintToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("workflow token: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Store exposes the hosting-side bindings to the worker service.
func (e *Engine) Store() *Store { return e.store }

// WorkerID is the coordinator's own computer.
func (e *Engine) WorkerID() string { return e.workerID }

// SetPlatform installs the platform. The worker wires it after the control
// router exists, which is after the service that owns the engine.
func (e *Engine) SetPlatform(p Platform) {
	e.platformMu.Lock()
	e.platform = p
	e.platformMu.Unlock()
	e.Wake()
}

func (e *Engine) currentPlatform() Platform {
	e.platformMu.RLock()
	defer e.platformMu.RUnlock()
	return e.platform
}

// Wake asks the loop for a pass now. It never blocks.
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Run drives the engine until ctx ends: a reconciling pass at start, a pass
// on every Wake, and a reconciling pass every interval.
func (e *Engine) Run(ctx context.Context) {
	e.Pass(ctx, true)
	ticker := e.clock.NewTicker(e.interval, "workflow", "reconcile")
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
			e.Pass(ctx, false)
		case <-ticker.C:
			e.Pass(ctx, true)
		}
	}
}

func (e *Engine) nowMs() int64 { return e.clock.Now().UnixMilli() }

func (e *Engine) workerName(id string) string {
	e.namesMu.RLock()
	defer e.namesMu.RUnlock()
	if name := e.names[id]; name != "" {
		return name
	}
	if id == "" {
		return "an unassigned computer"
	}
	return id
}

func (e *Engine) rememberNames(workers []WorkerCandidate) {
	e.namesMu.Lock()
	defer e.namesMu.Unlock()
	for _, w := range workers {
		if w.Name != "" {
			e.names[w.ID] = w.Name
		}
	}
}

// mutate runs fn as one transaction under the engine lock.
func (e *Engine) mutate(ctx context.Context, fn func(q querier) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.inTx(ctx, fn)
}

// --- Run state ---

type runState struct {
	userID    string
	run       *leapmuxv1.WorkflowRun
	roles     []*leapmuxv1.WorkflowRoleAssignment
	slots     []*leapmuxv1.WorkflowRunAgent
	tasks     []*leapmuxv1.WorkflowTask
	approvals []*leapmuxv1.WorkflowApproval
}

func loadRun(ctx context.Context, q querier, userID, runID string) (*runState, error) {
	run, err := getRun(ctx, q, userID, runID)
	if err != nil {
		return nil, err
	}
	rs := &runState{userID: userID, run: run}
	if rs.roles, err = listRoles(ctx, q, runID); err != nil {
		return nil, err
	}
	if rs.slots, err = listSlots(ctx, q, runID); err != nil {
		return nil, err
	}
	if rs.tasks, err = listTasks(ctx, q, runID); err != nil {
		return nil, err
	}
	if rs.approvals, err = listApprovals(ctx, q, runID); err != nil {
		return nil, err
	}
	return rs, nil
}

func (rs *runState) role(key string) *leapmuxv1.WorkflowRoleAssignment {
	for _, a := range rs.roles {
		if a.GetRoleKey() == key {
			return a
		}
	}
	return nil
}

func (rs *runState) slot(slotID string) *leapmuxv1.WorkflowRunAgent {
	for _, s := range rs.slots {
		if s.GetSlotId() == slotID {
			return s
		}
	}
	return nil
}

func (rs *runState) task(taskID string) *leapmuxv1.WorkflowTask {
	for _, t := range rs.tasks {
		if t.GetId() == taskID {
			return t
		}
	}
	return nil
}

func (rs *runState) approval(approvalID string) *leapmuxv1.WorkflowApproval {
	for _, a := range rs.approvals {
		if a.GetId() == approvalID {
			return a
		}
	}
	return nil
}

func (rs *runState) isLead(slot *leapmuxv1.WorkflowRunAgent) bool {
	return slot != nil && slot.GetSlotId() == rs.run.GetLeadSlotId()
}

func (rs *runState) detail(ctx context.Context, q querier) (*leapmuxv1.WorkflowRunDetail, error) {
	events, err := listRecentEvents(ctx, q, rs.run.GetId())
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.WorkflowRunDetail{
		Run: rs.run, Roles: rs.roles, Agents: rs.slots, Tasks: rs.tasks, Approvals: rs.approvals, Events: events,
	}, nil
}

// --- Status vocabulary ---

func runFinal(s leapmuxv1.WorkflowRunStatus) bool {
	switch s {
	case leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_FAILED,
		leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_CANCELLED,
		leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED:
		return true
	default:
		return false
	}
}

// runDispatches reports whether a run in this status may start work.
func runDispatches(s leapmuxv1.WorkflowRunStatus) bool {
	return s == leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING ||
		s == leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_WAITING
}

func taskFinal(s leapmuxv1.WorkflowTaskStatus) bool {
	switch s {
	case leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED,
		leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_FAILED,
		leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED:
		return true
	default:
		return false
	}
}

// taskInFlight reports a task whose instruction an agent holds.
func taskInFlight(s leapmuxv1.WorkflowTaskStatus) bool {
	return s == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_DISPATCHING ||
		s == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING
}

// taskOutstanding reports a task the run cannot complete around.
func taskOutstanding(s leapmuxv1.WorkflowTaskStatus) bool {
	switch s {
	case leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED,
		leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_AWAITING_APPROVAL,
		leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_DISPATCHING,
		leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING,
		leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUBMITTED,
		leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED:
		return true
	default:
		return false
	}
}

// slotHolds reports a slot that counts against the role's and run's limits.
func slotHolds(s leapmuxv1.WorkflowAgentStatus) bool {
	switch s {
	case leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING,
		leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE,
		leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_UNREACHABLE:
		return true
	default:
		return false
	}
}

// slotTakesWork reports a slot a new task may be assigned to.
func slotTakesWork(s leapmuxv1.WorkflowAgentStatus) bool {
	return s == leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING ||
		s == leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE
}

// --- Recording helpers (inside a mutate) ---

func (e *Engine) event(ctx context.Context, q querier, rs *runState, taskID string, kind leapmuxv1.WorkflowEventKind, actor, format string, args ...any) error {
	return insertEvent(ctx, q, &leapmuxv1.WorkflowEvent{
		RunId: rs.run.GetId(), TaskId: taskID, Kind: kind, Actor: actor, Message: fmt.Sprintf(format, args...), AtMs: e.nowMs(),
	})
}

// send records a message for a slot's agent. It is delivered by the next
// pass, at most once.
func (e *Engine) send(ctx context.Context, q querier, rs *runState, slotID, taskID, text string) error {
	if slotID == "" {
		return nil
	}
	return insertOutbox(ctx, q, outboxItem{id: e.newID(), runID: rs.run.GetId(), slotID: slotID, taskID: taskID, text: text}, e.nowMs())
}

func (e *Engine) notifyLead(ctx context.Context, q querier, rs *runState, format string, args ...any) error {
	if rs.run.GetLeadAgentId() == "" {
		return nil
	}
	return e.send(ctx, q, rs, rs.run.GetLeadSlotId(), "", leadNote(format, args...))
}

func (e *Engine) saveTask(ctx context.Context, q querier, t *leapmuxv1.WorkflowTask) error {
	t.UpdatedAtMs = e.nowMs()
	return updateTask(ctx, q, t)
}

func (e *Engine) saveRun(ctx context.Context, q querier, rs *runState) error {
	rs.run.UpdatedAtMs = e.nowMs()
	return updateRun(ctx, q, rs.run)
}

func (e *Engine) saveSlot(ctx context.Context, q querier, s *leapmuxv1.WorkflowRunAgent) error {
	return updateSlot(ctx, q, s, e.nowMs())
}

// setTaskStatus moves a task, records the move, and stamps its timestamps.
func (e *Engine) setTaskStatus(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, status leapmuxv1.WorkflowTaskStatus, actor, detail string) error {
	if t.GetStatus() == status && t.GetStatusDetail() == detail {
		return nil
	}
	moved := t.GetStatus() != status
	t.Status, t.StatusDetail = status, detail
	if taskFinal(status) {
		t.CompletedAtMs = e.nowMs()
		if err := e.releaseSlot(ctx, q, rs, t); err != nil {
			return err
		}
	}
	if err := e.saveTask(ctx, q, t); err != nil {
		return err
	}
	if !moved {
		return nil
	}
	msg := fmt.Sprintf("Task %q is now %s", t.GetTitle(), taskWord(status))
	if detail != "" {
		msg += ": " + detail
	}
	return e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_TASK, actor, "%s", msg)
}

// releaseSlot frees the agent a task held, so the role's next task can use it.
func (e *Engine) releaseSlot(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask) error {
	slot := rs.slot(t.GetSlotId())
	if slot == nil || slot.GetCurrentTaskId() != t.GetId() {
		return nil
	}
	slot.CurrentTaskId = ""
	return e.saveSlot(ctx, q, slot)
}

func taskWord(s leapmuxv1.WorkflowTaskStatus) string {
	name := strings.TrimPrefix(s.String(), "WORKFLOW_TASK_STATUS_")
	return strings.ToLower(strings.ReplaceAll(name, "_", " "))
}

func (e *Engine) requestApproval(ctx context.Context, q querier, rs *runState, kind leapmuxv1.WorkflowApprovalKind, taskID, title, details, by string) (*leapmuxv1.WorkflowApproval, error) {
	for _, a := range rs.approvals {
		if a.GetStatus() == leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING && a.GetKind() == kind && a.GetTaskId() == taskID && taskID != "" {
			return a, nil
		}
	}
	a := &leapmuxv1.WorkflowApproval{
		Id: e.newID(), RunId: rs.run.GetId(), TaskId: taskID, Kind: kind,
		Status: leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING,
		Title:  title, Details: details, RequestedBy: by, CreatedAtMs: e.nowMs(),
	}
	if err := insertApproval(ctx, q, a); err != nil {
		return nil, err
	}
	rs.approvals = append(rs.approvals, a)
	if err := e.event(ctx, q, rs, taskID, leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_APPROVAL, by, "Approval requested: %s", title); err != nil {
		return nil, err
	}
	return a, nil
}

func (rs *runState) approved(taskID string, kind leapmuxv1.WorkflowApprovalKind) bool {
	for _, a := range rs.approvals {
		if a.GetTaskId() == taskID && a.GetKind() == kind && a.GetStatus() == leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_APPROVED {
			return true
		}
	}
	return false
}

// settleRunStatus derives RUNNING / WAITING from pending approvals. It never
// overrides a status a person or a failure chose (paused, blocked, final).
func (e *Engine) settleRunStatus(ctx context.Context, q querier, rs *runState) error {
	if !runDispatches(rs.run.GetStatus()) {
		return nil
	}
	want := leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING
	detail := ""
	for _, a := range rs.approvals {
		if a.GetStatus() == leapmuxv1.WorkflowApprovalStatus_WORKFLOW_APPROVAL_STATUS_PENDING {
			want = leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_WAITING
			detail = "Waiting for approval: " + a.GetTitle()
			break
		}
	}
	return e.setRunStatus(ctx, q, rs, want, detail, "engine")
}

func (e *Engine) setRunStatus(ctx context.Context, q querier, rs *runState, status leapmuxv1.WorkflowRunStatus, detail, actor string) error {
	if rs.run.GetStatus() == status && rs.run.GetStatusDetail() == detail {
		return nil
	}
	moved := rs.run.GetStatus() != status
	rs.run.Status, rs.run.StatusDetail = status, detail
	if runFinal(status) && rs.run.GetCompletedAtMs() == 0 {
		rs.run.CompletedAtMs = e.nowMs()
	}
	if err := e.saveRun(ctx, q, rs); err != nil {
		return err
	}
	if !moved {
		return nil
	}
	word := strings.ToLower(strings.TrimPrefix(status.String(), "WORKFLOW_RUN_STATUS_"))
	msg := "Run is now " + word
	if detail != "" {
		msg += ": " + detail
	}
	return e.event(ctx, q, rs, "", leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_RUN, actor, "%s", msg)
}

// retryOrEscalate sends a task back for another attempt, or, out of attempts,
// blocks it and asks the user whether to grant one more.
func (e *Engine) retryOrEscalate(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, actor, reason string) error {
	if t.GetAttempt() < t.GetMaxAttempts() {
		t.ReviewFeedback = reason
		if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, actor,
			fmt.Sprintf("Retrying (attempt %d of %d)", t.GetAttempt()+1, t.GetMaxAttempts())); err != nil {
			return err
		}
		return nil
	}
	if err := e.releaseSlot(ctx, q, rs, t); err != nil {
		return err
	}
	if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, actor,
		fmt.Sprintf("Retry limit reached after %d attempt(s)", t.GetAttempt())); err != nil {
		return err
	}
	if _, err := e.requestApproval(ctx, q, rs, leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_ESCALATION, t.GetId(),
		fmt.Sprintf("Allow another attempt at %q?", t.GetTitle()),
		fmt.Sprintf("The task used all %d attempts. Last reason: %s", t.GetMaxAttempts(), reason), "engine"); err != nil {
		return err
	}
	return e.notifyLead(ctx, q, rs, "Task `%s` (%s) reached its retry limit and is escalated to the user: %s", t.GetId(), t.GetTitle(), reason)
}

// acceptResult finishes an accepted task, or holds it for a completion gate.
func (e *Engine) acceptResult(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, actor string) error {
	if t.GetApproval() == leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_COMPLETE &&
		!rs.approved(t.GetId(), leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_TASK_COMPLETION) {
		if _, err := e.requestApproval(ctx, q, rs, leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_TASK_COMPLETION, t.GetId(),
			fmt.Sprintf("Accept the result of %q?", t.GetTitle()), resultSummary(t.GetResult()), actor); err != nil {
			return err
		}
		return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_AWAITING_APPROVAL, actor, "Waiting for the user to accept the result")
	}
	return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, actor, "")
}

// --- The pass ---

// Pass advances every active run once: when reconcile is set it first checks
// each agent and computer, then it schedules ready tasks, opens the agents
// that scheduling reserved, and delivers recorded messages. Every step is
// safe to repeat, so a pass interrupted at any point is finished by the next.
func (e *Engine) Pass(ctx context.Context, reconcile bool) {
	platform := e.currentPlatform()
	if platform == nil {
		return
	}
	refs, err := activeRunIDs(ctx, e.store.db)
	if err != nil {
		e.log.Warn("list active workflow runs", "error", err)
		return
	}
	if len(refs) == 0 {
		return
	}
	workers, werr := platform.Workers(ctx)
	if werr != nil {
		e.log.Warn("list workers for workflow placement", "error", werr)
		workers = nil
	} else {
		e.rememberNames(workers)
	}
	for _, ref := range refs {
		if ctx.Err() != nil {
			return
		}
		if reconcile && werr == nil {
			e.reconcile(ctx, platform, ref, workers)
		}
		var toOpen []string
		if err := e.mutate(ctx, func(q querier) error {
			rs, err := loadRun(ctx, q, ref.userID, ref.runID)
			if err != nil {
				return err
			}
			toOpen, err = e.schedule(ctx, q, rs, workers, werr == nil)
			return err
		}); err != nil {
			e.log.Warn("schedule workflow run", "run", ref.runID, "error", err)
			continue
		}
		for _, slotID := range toOpen {
			e.openSlot(ctx, platform, ref, slotID)
		}
		e.deliver(ctx, platform, ref)
	}
}

// schedule assigns every ready task an agent, reserving new slots where a
// role has room, and records each dispatch message. It returns the reserved
// slots that still need opening.
func (e *Engine) schedule(ctx context.Context, q querier, rs *runState, workers []WorkerCandidate, workersKnown bool) ([]string, error) {
	run := rs.run
	if runDispatches(run.GetStatus()) {
		maxParallel := int(max(run.GetTemplate().GetMaxParallelTasks(), 1))
		inFlight := 0
		for _, t := range rs.tasks {
			if taskInFlight(t.GetStatus()) {
				inFlight++
			}
		}
		for _, t := range rs.tasks {
			if t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED {
				continue
			}
			if err := e.scheduleTask(ctx, q, rs, t, workers, workersKnown, maxParallel, &inFlight); err != nil {
				return nil, err
			}
		}
		if err := e.settleRunStatus(ctx, q, rs); err != nil {
			return nil, err
		}
	}
	var toOpen []string
	for _, s := range rs.slots {
		if s.GetStatus() != leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING || rs.isLead(s) {
			continue
		}
		if runFinal(run.GetStatus()) || run.GetStatus() == leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PAUSED {
			continue
		}
		if _, busy := e.opening.Load(s.GetSlotId()); !busy {
			toOpen = append(toOpen, s.GetSlotId())
		}
	}
	return toOpen, nil
}

func (e *Engine) scheduleTask(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, workers []WorkerCandidate, workersKnown bool, maxParallel int, inFlight *int) error {
	wait := func(detail string) error {
		if t.GetStatusDetail() == detail {
			return nil
		}
		t.StatusDetail = detail
		return e.saveTask(ctx, q, t)
	}
	var pendingDeps []string
	for _, depID := range t.GetDependsOn() {
		dep := rs.task(depID)
		if dep == nil {
			continue
		}
		switch dep.GetStatus() {
		case leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED:
		case leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_FAILED, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED:
			if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, "engine",
				fmt.Sprintf("Dependency %q did not succeed", dep.GetTitle())); err != nil {
				return err
			}
			return e.notifyLead(ctx, q, rs, "Task `%s` (%s) is blocked: its dependency `%s` (%s) is %s.",
				t.GetId(), t.GetTitle(), dep.GetId(), dep.GetTitle(), taskWord(dep.GetStatus()))
		default:
			pendingDeps = append(pendingDeps, dep.GetTitle())
		}
	}
	if len(pendingDeps) > 0 {
		return wait("Waiting for " + strings.Join(pendingDeps, ", "))
	}
	if t.GetApproval() == leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_START &&
		!rs.approved(t.GetId(), leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_TASK_START) {
		if _, err := e.requestApproval(ctx, q, rs, leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_TASK_START, t.GetId(),
			fmt.Sprintf("Start %q?", t.GetTitle()), t.GetInstructions(), "engine"); err != nil {
			return err
		}
		if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_AWAITING_APPROVAL, "engine", "Waiting for the user to approve the start"); err != nil {
			return err
		}
		return e.notifyLead(ctx, q, rs, "Task `%s` (%s) waits for human approval before it starts.", t.GetId(), t.GetTitle())
	}
	if *inFlight >= maxParallel {
		return wait(fmt.Sprintf("Waiting for a free task slot (%d running, limit %d)", *inFlight, maxParallel))
	}
	assignment := rs.role(t.GetRoleKey())
	if assignment == nil {
		return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, "engine", "Its role no longer exists")
	}
	slot, err := e.pickSlot(ctx, q, rs, t, assignment, workers, workersKnown, wait)
	if err != nil || slot == nil {
		return err
	}
	*inFlight++
	return e.dispatch(ctx, q, rs, t, slot)
}

// pickSlot finds or reserves the agent for a task. It returns nil (having
// recorded why) when the task must wait.
func (e *Engine) pickSlot(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, assignment *leapmuxv1.WorkflowRoleAssignment, workers []WorkerCandidate, workersKnown bool, wait func(string) error) (*leapmuxv1.WorkflowRunAgent, error) {
	free := func(s *leapmuxv1.WorkflowRunAgent) bool {
		return s.GetRoleKey() == t.GetRoleKey() && slotTakesWork(s.GetStatus()) && !rs.isLead(s) &&
			(s.GetCurrentTaskId() == "" || s.GetCurrentTaskId() == t.GetId()) &&
			(t.GetWorkerId() == "" || s.GetWorkerId() == t.GetWorkerId())
	}
	// The agent that worked on this task before keeps its context, so a
	// correction goes back to it while it is still there.
	if prev := rs.slot(t.GetSlotId()); prev != nil && free(prev) {
		return prev, nil
	}
	for _, s := range rs.slots {
		if free(s) {
			return s, nil
		}
	}
	role := assignment.GetRole()
	held, roleHeld := 0, 0
	for _, s := range rs.slots {
		if slotHolds(s.GetStatus()) {
			held++
			if s.GetRoleKey() == t.GetRoleKey() {
				roleHeld++
			}
		}
	}
	if roleHeld >= int(max(role.GetAgent().GetMaxInstances(), 1)) {
		return nil, wait(fmt.Sprintf("Waiting for the %s agent to finish its current task", role.GetName()))
	}
	if held >= int(max(rs.run.GetTemplate().GetMaxAgents(), 1)) {
		return nil, wait(fmt.Sprintf("Waiting: the run already has its limit of %d agents", rs.run.GetTemplate().GetMaxAgents()))
	}
	if !workersKnown {
		return nil, wait("Waiting for the list of computers")
	}
	placement := role.GetAgent().GetPlacement()
	if t.GetWorkerId() != "" {
		placement = &leapmuxv1.WorkerPlacement{Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER, WorkerId: t.GetWorkerId()}
	}
	placed, err := Place(placement, workers, assignment.GetWorkerId())
	if err != nil {
		if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, "engine", err.Error()); err != nil {
			return nil, err
		}
		return nil, e.notifyLead(ctx, q, rs, "Task `%s` (%s) is blocked: %s. The user can reassign it.", t.GetId(), t.GetTitle(), err)
	}
	if placed.Waiting != "" {
		return nil, wait(placed.Waiting)
	}
	token := e.newToken()
	slot := &leapmuxv1.WorkflowRunAgent{
		SlotId: e.newID(), RunId: rs.run.GetId(), RoleKey: t.GetRoleKey(), WorkerId: placed.WorkerID,
		Status: leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING, StatusDetail: "Opening the agent",
		CreatedAtMs: e.nowMs(),
	}
	if err := insertSlot(ctx, q, slot, token, e.nowMs()); err != nil {
		return nil, err
	}
	rs.slots = append(rs.slots, slot)
	if assignment.GetWorkerId() != placed.WorkerID && t.GetWorkerId() == "" {
		assignment.WorkerId = placed.WorkerID
		if err := putRole(ctx, q, rs.run.GetId(), assignment); err != nil {
			return nil, err
		}
	}
	if err := e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_PLACEMENT, "engine",
		"Placed a %s agent on %s", role.GetName(), e.workerName(placed.WorkerID)); err != nil {
		return nil, err
	}
	return slot, nil
}

// dispatch records the task's instruction for its agent. The task stays
// DISPATCHING until that message is delivered.
func (e *Engine) dispatch(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, slot *leapmuxv1.WorkflowRunAgent) error {
	assignment := rs.role(t.GetRoleKey())
	briefed, err := slotHasMessages(ctx, q, slot.GetSlotId())
	if err != nil {
		return err
	}
	t.Attempt++
	t.SlotId = slot.GetSlotId()
	if t.GetStartedAtMs() == 0 {
		t.StartedAtMs = e.nowMs()
	}
	slot.CurrentTaskId = t.GetId()
	if err := e.saveSlot(ctx, q, slot); err != nil {
		return err
	}
	if err := e.send(ctx, q, rs, slot.GetSlotId(), t.GetId(), taskDispatch(rs.run, assignment.GetRole(), t, !briefed)); err != nil {
		return err
	}
	return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_DISPATCHING, "engine",
		fmt.Sprintf("Sending to the %s agent on %s", assignment.GetRole().GetName(), e.workerName(slot.GetWorkerId())))
}

// openSlot opens the agent for a reserved slot, or adopts one a previous
// attempt already opened, and records the outcome.
func (e *Engine) openSlot(ctx context.Context, platform Platform, ref runRef, slotID string) {
	if _, busy := e.opening.LoadOrStore(slotID, struct{}{}); busy {
		return
	}
	defer e.opening.Delete(slotID)

	var rs *runState
	var token string
	if err := e.store.inTx(ctx, func(q querier) error {
		var err error
		if rs, err = loadRun(ctx, q, ref.userID, ref.runID); err != nil {
			return err
		}
		token, err = slotToken(ctx, q, slotID)
		return err
	}); err != nil {
		e.log.Warn("load workflow slot", "slot", slotID, "error", err)
		return
	}
	slot := rs.slot(slotID)
	if slot == nil || slot.GetStatus() != leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING {
		return
	}
	assignment := rs.role(slot.GetRoleKey())
	if assignment == nil {
		return
	}

	// A previous process may have opened this slot's agent and died before
	// recording it. The hosting worker remembers every agent it opened for a
	// run, so ask before opening a second one.
	existing, err := platform.FindAgents(ctx, slot.GetWorkerId(), rs.run.GetId())
	if err != nil {
		e.recordOpenFailure(ctx, ref, slotID, err)
		return
	}
	agentID := existing[slotID]
	if agentID == "" {
		agentID, err = platform.OpenAgent(ctx, slot.GetWorkerId(), e.openRequest(rs, assignment.GetRole(), slot, token))
		if err != nil {
			e.recordOpenFailure(ctx, ref, slotID, err)
			return
		}
	}
	tabErr := platform.AddAgentTab(ctx, rs.run.GetWorkspaceId(), slot.GetWorkerId(), agentID)
	if err := e.mutate(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, ref.userID, ref.runID)
		if err != nil {
			return err
		}
		slot := rs.slot(slotID)
		if slot == nil {
			return nil
		}
		slot.AgentId = agentID
		slot.Status = leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE
		slot.StatusDetail = ""
		if tabErr != nil {
			slot.StatusDetail = "Not shown as a tab: " + tabErr.Error()
		}
		if err := e.saveSlot(ctx, q, slot); err != nil {
			return err
		}
		return e.event(ctx, q, rs, slot.GetCurrentTaskId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_AGENT, "engine",
			"Started the %s agent on %s", assignment.GetRole().GetName(), e.workerName(slot.GetWorkerId()))
	}); err != nil {
		e.log.Warn("record opened workflow agent", "slot", slotID, "error", err)
	}
}

func (e *Engine) openRequest(rs *runState, role *leapmuxv1.WorkflowRole, slot *leapmuxv1.WorkflowRunAgent, token string) *leapmuxv1.OpenAgentRequest {
	agent := role.GetAgent()
	workingDir := agent.GetWorkingDir()
	if workingDir == "" {
		workingDir = rs.run.GetWorkingDir()
	}
	req := &leapmuxv1.OpenAgentRequest{
		WorkerId:      slot.GetWorkerId(),
		WorkingDir:    workingDir,
		AgentProvider: agent.GetAgentProvider(),
		Options:       agent.GetOptions(),
		Title:         fmt.Sprintf("%s: %s", rs.run.GetName(), role.GetName()),
		Workflow: &leapmuxv1.WorkflowBinding{
			RunId: rs.run.GetId(), SlotId: slot.GetSlotId(), RoleKey: role.GetKey(),
			CoordinatorWorkerId: e.workerID, Token: token, Confine: rs.run.GetTemplate().GetConfineAgents(),
		},
	}
	if role.GetGitIsolation() == leapmuxv1.WorkflowGitIsolation_WORKFLOW_GIT_ISOLATION_WORKTREE {
		req.CreateWorktree = true
		req.WorktreeBranch = WorktreeBranch(rs.run.GetId(), role.GetKey(), slot.GetSlotId())
		req.WorktreeBaseBranch = rs.run.GetBaseBranch()
	}
	return req
}

// WorktreeBranch names the run-scoped branch of a role's isolated worktree.
// The slot suffix keeps two instances of one role apart.
func WorktreeBranch(runID, roleKey, slotID string) string {
	return fmt.Sprintf("wf/%s/%s-%s", shortID(runID, 8), roleKey, shortID(slotID, 6))
}

func shortID(s string, n int) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(clean) > n {
		clean = clean[:n]
	}
	return strings.ToLower(clean)
}

// recordOpenFailure keeps a transiently failed slot STARTING for the next
// pass, and gives up on one that can never open, blocking its task.
func (e *Engine) recordOpenFailure(ctx context.Context, ref runRef, slotID string, cause error) {
	var permanent *PermanentError
	isPermanent := errors.As(cause, &permanent)
	if err := e.mutate(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, ref.userID, ref.runID)
		if err != nil {
			return err
		}
		slot := rs.slot(slotID)
		if slot == nil || slot.GetStatus() != leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING {
			return nil
		}
		where := e.workerName(slot.GetWorkerId())
		if !isPermanent {
			slot.StatusDetail = fmt.Sprintf("Retrying the start on %s: %v", where, cause)
			return e.saveSlot(ctx, q, slot)
		}
		slot.Status = leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_LOST
		slot.StatusDetail = fmt.Sprintf("Could not start on %s: %v", where, cause)
		if err := e.saveSlot(ctx, q, slot); err != nil {
			return err
		}
		if err := e.event(ctx, q, rs, slot.GetCurrentTaskId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_ERROR, "engine", "%s", slot.GetStatusDetail()); err != nil {
			return err
		}
		return e.slotLost(ctx, q, rs, slot, true)
	}); err != nil {
		e.log.Warn("record workflow open failure", "slot", slotID, "error", err)
	}
}

// slotLost requeues or blocks the work a vanished agent held. A start that
// can never succeed blocks the task instead of retrying it on the same
// computer, which would fail the same way.
func (e *Engine) slotLost(ctx context.Context, q querier, rs *runState, slot *leapmuxv1.WorkflowRunAgent, startFailed bool) error {
	if err := e.dropOutbox(ctx, q, rs, slot.GetSlotId(), "agent gone"); err != nil {
		return err
	}
	if rs.isLead(slot) {
		if runFinal(rs.run.GetStatus()) {
			return nil
		}
		return e.setRunStatus(ctx, q, rs, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_BLOCKED,
			"The Lead agent is gone (closed, or it failed). Open a new Lead or cancel the run.", "engine")
	}
	for _, t := range rs.tasks {
		if t.GetSlotId() != slot.GetSlotId() || !taskInFlight(t.GetStatus()) {
			continue
		}
		if startFailed {
			if err := e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, "engine", slot.GetStatusDetail()); err != nil {
				return err
			}
			if err := e.notifyLead(ctx, q, rs, "Task `%s` (%s) is blocked: %s. The user can retry or reassign it.", t.GetId(), t.GetTitle(), slot.GetStatusDetail()); err != nil {
				return err
			}
			continue
		}
		t.SlotId = ""
		if err := e.retryOrEscalate(ctx, q, rs, t, "engine", "The agent working on it was lost: "+slot.GetStatusDetail()); err != nil {
			return err
		}
		if err := e.notifyLead(ctx, q, rs, "The agent on task `%s` (%s) was lost (%s); the engine re-queued the task.", t.GetId(), t.GetTitle(), slot.GetStatusDetail()); err != nil {
			return err
		}
	}
	slot.CurrentTaskId = ""
	return e.saveSlot(ctx, q, slot)
}

func (e *Engine) dropOutbox(ctx context.Context, q querier, rs *runState, slotID, why string) error {
	items, err := pendingOutbox(ctx, q, rs.run.GetId())
	if err != nil {
		return err
	}
	for _, it := range items {
		if slotID == "" || it.slotID == slotID {
			if err := markOutbox(ctx, q, it.id, -1, why); err != nil {
				return err
			}
		}
	}
	return nil
}

// deliver sends each recorded message whose agent is ready, in order per
// agent. A failed send stops that agent's queue until the next pass.
func (e *Engine) deliver(ctx context.Context, platform Platform, ref runRef) {
	var rs *runState
	var items []outboxItem
	if err := e.store.inTx(ctx, func(q querier) error {
		var err error
		if rs, err = loadRun(ctx, q, ref.userID, ref.runID); err != nil {
			return err
		}
		items, err = pendingOutbox(ctx, q, ref.runID)
		return err
	}); err != nil {
		return
	}
	if rs.run.GetStatus() == leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PAUSED || runFinal(rs.run.GetStatus()) {
		return
	}
	stalled := map[string]bool{}
	for _, it := range items {
		if stalled[it.slotID] {
			continue
		}
		slot := rs.slot(it.slotID)
		if slot == nil || slot.GetStatus() != leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE || slot.GetAgentId() == "" {
			stalled[it.slotID] = true
			continue
		}
		sendErr := platform.EnqueueInput(ctx, slot.GetWorkerId(), slot.GetAgentId(), it.id, it.text)
		if sendErr != nil {
			stalled[it.slotID] = true
		}
		if err := e.mutate(ctx, func(q querier) error {
			if sendErr != nil {
				return markOutbox(ctx, q, it.id, 0, sendErr.Error())
			}
			if err := markOutbox(ctx, q, it.id, e.nowMs(), ""); err != nil {
				return err
			}
			if it.taskID == "" {
				return nil
			}
			rs, err := loadRun(ctx, q, ref.userID, ref.runID)
			if err != nil {
				return err
			}
			t := rs.task(it.taskID)
			if t == nil || t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_DISPATCHING {
				return nil
			}
			return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, "engine",
				"With the agent on "+e.workerName(slot.GetWorkerId()))
		}); err != nil {
			e.log.Warn("record workflow delivery", "item", it.id, "error", err)
			return
		}
	}
}

// reconcile asks every computer about the run's agents and records what
// changed: a computer that went away, one that came back, an agent that is
// gone. It runs before scheduling, so lost work is re-queued in the same pass.
func (e *Engine) reconcile(ctx context.Context, platform Platform, ref runRef, workers []WorkerCandidate) {
	var slots []*leapmuxv1.WorkflowRunAgent
	if err := e.store.inTx(ctx, func(q querier) error {
		var err error
		slots, err = listSlots(ctx, q, ref.runID)
		return err
	}); err != nil {
		return
	}
	online := map[string]bool{}
	for _, w := range workers {
		online[w.ID] = w.Online
	}
	byWorker := map[string][]string{}
	for _, s := range slots {
		if s.GetAgentId() == "" {
			continue
		}
		switch s.GetStatus() {
		case leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE, leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_UNREACHABLE:
			byWorker[s.GetWorkerId()] = append(byWorker[s.GetWorkerId()], s.GetAgentId())
		default:
			// A starting slot has no agent to ask about; a gone one is final.
		}
	}
	observed := map[string]AgentState{}
	unreachable := map[string]string{}
	for workerID, agentIDs := range byWorker {
		if !online[workerID] {
			unreachable[workerID] = e.workerName(workerID) + " is offline"
			continue
		}
		states, err := platform.AgentStates(ctx, workerID, agentIDs)
		if err != nil {
			unreachable[workerID] = fmt.Sprintf("%s did not answer: %v", e.workerName(workerID), err)
			continue
		}
		for _, agentID := range agentIDs {
			state, ok := states[agentID]
			if !ok {
				state = AgentGone
			}
			observed[agentID] = state
		}
	}
	if err := e.mutate(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, ref.userID, ref.runID)
		if err != nil {
			return err
		}
		for _, s := range rs.slots {
			if s.GetAgentId() == "" || !slotHolds(s.GetStatus()) {
				continue
			}
			if why, ok := unreachable[s.GetWorkerId()]; ok {
				if s.GetStatus() == leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_UNREACHABLE {
					continue
				}
				s.Status, s.StatusDetail = leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_UNREACHABLE, why
				if err := e.saveSlot(ctx, q, s); err != nil {
					return err
				}
				if err := e.event(ctx, q, rs, s.GetCurrentTaskId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_AGENT, "engine", "Agent unreachable: %s", why); err != nil {
					return err
				}
				continue
			}
			state, ok := observed[s.GetAgentId()]
			if !ok {
				continue
			}
			switch state {
			case AgentAlive:
				if s.GetStatus() == leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_UNREACHABLE {
					s.Status, s.StatusDetail = leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE, ""
					if err := e.saveSlot(ctx, q, s); err != nil {
						return err
					}
					if err := e.event(ctx, q, rs, s.GetCurrentTaskId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_AGENT, "engine",
						"Agent on %s is reachable again", e.workerName(s.GetWorkerId())); err != nil {
						return err
					}
				}
			case AgentGone:
				s.Status = leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_LOST
				s.StatusDetail = "The agent was closed or is no longer on " + e.workerName(s.GetWorkerId())
				if err := e.saveSlot(ctx, q, s); err != nil {
					return err
				}
				if err := e.event(ctx, q, rs, s.GetCurrentTaskId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_AGENT, "engine", "%s", s.GetStatusDetail()); err != nil {
					return err
				}
				if err := e.slotLost(ctx, q, rs, s, false); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		e.log.Warn("reconcile workflow run", "run", ref.runID, "error", err)
	}
}

// interruptAll stops the current turn of every live agent of a run. Best
// effort: an unreachable computer is told nothing, and its agent's next
// message is never delivered because the run is final.
func (e *Engine) interruptAll(ctx context.Context, slots []*leapmuxv1.WorkflowRunAgent) {
	platform := e.currentPlatform()
	if platform == nil {
		return
	}
	for _, s := range slots {
		if s.GetAgentId() == "" || !slotHolds(s.GetStatus()) {
			continue
		}
		if err := platform.InterruptAgent(ctx, s.GetWorkerId(), s.GetAgentId()); err != nil {
			e.log.Info("interrupt workflow agent", "agent", s.GetAgentId(), "error", err)
		}
	}
}
