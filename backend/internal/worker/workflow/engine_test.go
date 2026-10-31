package workflow_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

const (
	userID      = "user-1"
	coordinator = "w-lead"
	builderPC   = "w-builder"
	macPC       = "w-mac"
)

type harness struct {
	t        *testing.T
	ctx      context.Context
	db       *sql.DB
	platform *fakePlatform
	engine   *workflow.Engine
	runID    string
	leadAuth *leapmuxv1.WorkflowAgentAuth
	leadID   string
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := workerdb.Open(filepath.Join(t.TempDir(), "worker.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), db))
	return db
}

func newEngine(t *testing.T, db *sql.DB, platform workflow.Platform) *workflow.Engine {
	t.Helper()
	e := workflow.New(workflow.Config{
		DB: db, WorkerID: coordinator, Clock: testutil.NewQuartzMock(t),
		Capabilities: func() []string { return []string{"os:linux"} },
	})
	e.SetPlatform(platform)
	return e
}

// team is a Lead anywhere, a Builder pinned to one computer that must report
// a commit and tests and is reviewed, and an iOS tester placed by capability.
func team() *leapmuxv1.WorkflowTemplate {
	return &leapmuxv1.WorkflowTemplate{
		Name: "Ship it",
		Roles: []*leapmuxv1.WorkflowRole{
			{Key: "lead", Name: "Lead", IsLead: true, Agent: &leapmuxv1.AgentProfile{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}},
			{
				Key: "builder", Name: "Builder", ReviewRequired: true,
				RequiredOutputs: []string{"summary", "commit", "tests"},
				GitIsolation:    leapmuxv1.WorkflowGitIsolation_WORKFLOW_GIT_ISOLATION_WORKTREE,
				Agent: &leapmuxv1.AgentProfile{
					AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
					Options:       map[string]string{"effort": "high"},
					Placement: &leapmuxv1.WorkerPlacement{
						Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER, WorkerId: builderPC,
					},
				},
			},
			{
				Key: "ios", Name: "iOS Tester", RequiredOutputs: []string{"tests"},
				Agent: &leapmuxv1.AgentProfile{
					AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
					Placement: &leapmuxv1.WorkerPlacement{
						Strategy:             leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY,
						RequiredCapabilities: []string{"os:darwin", "xcode"},
					},
				},
			},
		},
		Steps: []*leapmuxv1.WorkflowStep{
			{Key: "implement", Title: "Planned implement", RoleKey: "builder", Instructions: "Build the feature."},
			{Key: "merge", Title: "Planned merge", RoleKey: "builder", DependsOn: []string{"implement"},
				Approval: leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_START, Instructions: "Merge it."},
		},
		DefaultMaxAttempts: 2,
		MaxParallelTasks:   2,
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, ctx: t.Context(), db: openDB(t), platform: newFakePlatform()}
	h.platform.addWorker(coordinator, "Home PC", true, "os:linux")
	h.platform.addWorker(builderPC, "Windows VM", true, "os:windows")
	h.platform.addWorker(macPC, "Mac mini", true, "os:darwin", "xcode")
	h.engine = newEngine(t, h.db, h.platform)
	h.launch()
	return h
}

// launch does what the frontend does: create the run, open the Lead with
// its binding on the coordinator, and attach it.
func (h *harness) launch() {
	resp, err := h.engine.CreateRun(h.ctx, userID, &leapmuxv1.CreateWorkflowRunRequest{
		Template: team(), Objective: "Add dark mode", WorkingDir: "/repo", WorkspaceId: "ws-1", BaseBranch: "main",
	})
	require.NoError(h.t, err)
	assert.Equal(h.t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_PENDING, resp.GetRun().GetStatus())
	h.runID = resp.GetRun().GetId()
	h.leadAuth = &leapmuxv1.WorkflowAgentAuth{RunId: h.runID, Token: resp.GetLeadBinding().GetToken()}
	h.leadID = "lead-agent"
	_, err = h.db.ExecContext(h.ctx, `INSERT INTO agents (id) VALUES (?)`, h.leadID)
	require.NoError(h.t, err)
	require.NoError(h.t, h.engine.Store().PutBinding(h.ctx, h.leadID, resp.GetLeadBinding()))
	h.platform.hostLead(coordinator, h.leadID)
	detail, err := h.engine.AttachLead(h.ctx, userID, h.runID, h.leadID)
	require.NoError(h.t, err)
	assert.Equal(h.t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, detail.GetRun().GetStatus())
}

func (h *harness) pass() { h.engine.Pass(h.ctx, true) }

func (h *harness) detail() *leapmuxv1.WorkflowRunDetail {
	d, err := h.engine.GetRun(h.ctx, userID, h.runID)
	require.NoError(h.t, err)
	return d
}

func (h *harness) task(title string) *leapmuxv1.WorkflowTask {
	for _, t := range h.detail().GetTasks() {
		if t.GetTitle() == title {
			return t
		}
	}
	h.t.Fatalf("no task %q", title)
	return nil
}

func (h *harness) delegate(role, title string) *leapmuxv1.WorkflowTask {
	t, err := h.engine.Delegate(h.ctx, userID, &leapmuxv1.WorkflowAgentDelegateRequest{
		Auth: h.leadAuth, RoleKey: role, Title: title, Instructions: "Do " + title + ".",
	})
	require.NoError(h.t, err)
	return t
}

// authOf is the credential the engine handed the agent opened on workerID.
func (h *harness) authOf(workerID string) (*leapmuxv1.WorkflowAgentAuth, string) {
	for _, c := range h.platform.openCalls() {
		if c.workerID == workerID {
			for _, a := range h.detail().GetAgents() {
				if a.GetSlotId() == c.req.GetWorkflow().GetSlotId() {
					return &leapmuxv1.WorkflowAgentAuth{RunId: h.runID, Token: c.req.GetWorkflow().GetToken()}, a.GetAgentId()
				}
			}
		}
	}
	h.t.Fatalf("no agent opened on %s", workerID)
	return nil, ""
}

func goodBuild() *leapmuxv1.WorkflowTaskResult {
	return &leapmuxv1.WorkflowTaskResult{
		Outcome: leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED, Summary: "Implemented.",
		GitBranch: "wf/x/builder", GitCommit: "0123abc", TestsRan: true, TestsPassed: true, TestSummary: "go test ./... ok",
	}
}

func TestLeadIsBriefedAndPlannedStepsWait(t *testing.T) {
	h := newHarness(t)
	h.pass()
	msgs := h.platform.messagesTo(h.leadID)
	require.Len(t, msgs, 1, "the Lead receives its briefing once")
	assert.Contains(t, msgs[0], "Add dark mode")
	assert.Contains(t, msgs[0], "leapmux control workflow delegate")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_PENDING, h.task("Planned implement").GetStatus(),
		"a template step waits for the Lead to release it")
	assert.Empty(t, h.platform.openCalls(), "no agent starts before the Lead delegates")

	h.pass()
	assert.Len(t, h.platform.messagesTo(h.leadID), 1, "a second pass resends nothing")
}

func TestDelegationOpensTheAgentOnItsPinnedComputer(t *testing.T) {
	h := newHarness(t)
	task := h.delegate("builder", "Implement dark mode")
	h.pass()

	calls := h.platform.openCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, builderPC, calls[0].workerID, "a pinned role opens on its pinned computer")
	req := calls[0].req
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, req.GetAgentProvider())
	assert.Equal(t, "high", req.GetOptions()["effort"])
	assert.True(t, req.GetCreateWorktree(), "a worktree role gets its own checkout")
	assert.True(t, strings.HasPrefix(req.GetWorktreeBranch(), "wf/"))
	assert.Equal(t, "main", req.GetWorktreeBaseBranch())
	assert.Equal(t, coordinator, req.GetWorkflow().GetCoordinatorWorkerId())
	assert.Equal(t, "builder", req.GetWorkflow().GetRoleKey())

	_, agentID := h.authOf(builderPC)
	assert.Equal(t, "ws-1", h.platform.tabs[agentID], "the agent appears as a tab in the run's workspace")
	got := h.task(task.GetTitle())
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, got.GetStatus())
	assert.Equal(t, uint32(1), got.GetAttempt())
	msgs := h.platform.messagesTo(agentID)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0], "You are the **Builder**")
	assert.Contains(t, msgs[0], "--commit <sha>", "the dispatch names the outputs the role must report")
}

func TestPinnedRoleWaitsForItsOfflineComputerAndNeverMoves(t *testing.T) {
	h := newHarness(t)
	h.platform.setOnline(builderPC, false)
	task := h.delegate("builder", "Implement")
	h.pass()
	h.pass()

	assert.Empty(t, h.platform.openCalls(), "no other computer is used for a pinned role")
	got := h.task(task.GetTitle())
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, got.GetStatus())
	assert.Contains(t, got.GetStatusDetail(), "Windows VM")

	h.platform.setOnline(builderPC, true)
	h.pass()
	calls := h.platform.openCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, builderPC, calls[0].workerID)
}

func TestCapabilityPlacementChoosesAQualifyingComputer(t *testing.T) {
	h := newHarness(t)
	h.delegate("ios", "Simulator tests")
	h.pass()
	calls := h.platform.openCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, macPC, calls[0].workerID, "only the Mac has os:darwin and xcode")
}

func TestOnlyTheLeadDelegatesAndOnlyTheAssigneeReports(t *testing.T) {
	h := newHarness(t)
	task := h.delegate("builder", "Implement")
	h.pass()
	builderAuth, _ := h.authOf(builderPC)

	_, err := h.engine.Delegate(h.ctx, userID, &leapmuxv1.WorkflowAgentDelegateRequest{
		Auth: builderAuth, RoleKey: "ios", Title: "Sneaky", Instructions: "x",
	})
	assert.ErrorIs(t, err, workflow.ErrDenied, "an assignee cannot delegate")

	_, err = h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: h.leadAuth, TaskId: task.GetId(), Result: goodBuild()})
	assert.ErrorIs(t, err, workflow.ErrDenied, "the Lead cannot report a task on the assignee's behalf")

	forged := &leapmuxv1.WorkflowAgentAuth{RunId: h.runID, Token: "forged"}
	_, err = h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: forged, TaskId: task.GetId(), Result: goodBuild()})
	assert.ErrorIs(t, err, workflow.ErrDenied)

	other := &leapmuxv1.WorkflowAgentAuth{RunId: "another-run", Token: builderAuth.GetToken()}
	_, err = h.engine.AgentContext(h.ctx, userID, other)
	assert.ErrorIs(t, err, workflow.ErrDenied, "a credential reaches only its own run")

	_, err = h.engine.AgentContext(h.ctx, "someone-else", builderAuth)
	assert.ErrorIs(t, err, workflow.ErrDenied, "a credential reaches only its user's run")
}

func TestReportMustCarryTheRequiredEvidence(t *testing.T) {
	h := newHarness(t)
	task := h.delegate("builder", "Implement")
	h.pass()
	auth, _ := h.authOf(builderPC)

	noCommit := goodBuild()
	noCommit.GitCommit = ""
	_, err := h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: task.GetId(), Result: noCommit})
	require.ErrorIs(t, err, workflow.ErrInvalid)
	assert.Contains(t, err.Error(), "commit")

	failing := goodBuild()
	failing.TestsPassed = false
	_, err = h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: task.GetId(), Result: failing})
	require.ErrorIs(t, err, workflow.ErrInvalid, "a completed report with failing tests is refused")

	bad := goodBuild()
	bad.GitCommit = "not-a-sha"
	_, err = h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: task.GetId(), Result: bad})
	require.ErrorIs(t, err, workflow.ErrInvalid)

	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, h.task("Implement").GetStatus(), "refused reports change nothing")

	_, err = h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: task.GetId(), Result: goodBuild()})
	require.NoError(t, err)
	got := h.task("Implement")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUBMITTED, got.GetStatus(), "a reviewed role waits for the Lead")
	assert.Equal(t, "0123abc", got.GetResult().GetGitCommit())

	h.pass()
	var reviewNote bool
	for _, m := range h.platform.messagesTo(h.leadID) {
		reviewNote = reviewNote || strings.Contains(m, "needs your review")
	}
	assert.True(t, reviewNote, "the Lead is told the result waits for review")
}

func TestRejectedResultReturnsToTheSameAgentThenEscalates(t *testing.T) {
	h := newHarness(t)
	task := h.delegate("builder", "Implement")
	h.pass()
	auth, agentID := h.authOf(builderPC)

	report := func() {
		_, err := h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: task.GetId(), Result: goodBuild()})
		require.NoError(t, err)
	}
	review := func(feedback string) {
		_, err := h.engine.Review(h.ctx, userID, &leapmuxv1.WorkflowAgentReviewRequest{Auth: h.leadAuth, TaskId: task.GetId(), Feedback: feedback})
		require.NoError(t, err)
	}

	report()
	review("Handle the system theme too.")
	h.pass()
	got := h.task("Implement")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, got.GetStatus())
	assert.Equal(t, uint32(2), got.GetAttempt())
	assert.Len(t, h.platform.openCalls(), 1, "the correction goes to the agent that holds the context")
	var correction bool
	for _, m := range h.platform.messagesTo(agentID) {
		correction = correction || strings.Contains(m, "Handle the system theme too.")
	}
	assert.True(t, correction)

	report()
	review("Still wrong.")
	got = h.task("Implement")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, got.GetStatus(), "out of attempts")
	var escalation *leapmuxv1.WorkflowApproval
	for _, a := range h.detail().GetApprovals() {
		if a.GetKind() == leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_ESCALATION {
			escalation = a
		}
	}
	require.NotNil(t, escalation, "the retry limit escalates to the user")
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_WAITING, h.detail().GetRun().GetStatus())

	_, err := h.engine.DecideApproval(h.ctx, userID, &leapmuxv1.DecideWorkflowApprovalRequest{RunId: h.runID, ApprovalId: escalation.GetId(), Approve: true})
	require.NoError(t, err)
	h.pass()
	got = h.task("Implement")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, got.GetStatus())
	assert.Equal(t, uint32(3), got.GetAttempt())
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, h.detail().GetRun().GetStatus())
}

func TestApprovalGateHoldsATaskUntilTheUserDecides(t *testing.T) {
	h := newHarness(t)
	implement, merge := h.task("Planned implement"), h.task("Planned merge")
	_, err := h.engine.Delegate(h.ctx, userID, &leapmuxv1.WorkflowAgentDelegateRequest{Auth: h.leadAuth, TaskId: implement.GetId()})
	require.NoError(t, err)
	_, err = h.engine.Delegate(h.ctx, userID, &leapmuxv1.WorkflowAgentDelegateRequest{Auth: h.leadAuth, TaskId: merge.GetId()})
	require.NoError(t, err)
	h.pass()
	assert.Contains(t, h.task("Planned merge").GetStatusDetail(), "Planned implement", "the merge waits for its dependency")

	auth, _ := h.authOf(builderPC)
	_, err = h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: implement.GetId(), Result: goodBuild()})
	require.NoError(t, err)
	_, err = h.engine.Review(h.ctx, userID, &leapmuxv1.WorkflowAgentReviewRequest{Auth: h.leadAuth, TaskId: implement.GetId(), Accept: true})
	require.NoError(t, err)
	h.pass()

	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_AWAITING_APPROVAL, h.task("Planned merge").GetStatus())
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_WAITING, h.detail().GetRun().GetStatus())
	h.pass()
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_AWAITING_APPROVAL, h.task("Planned merge").GetStatus(), "passes never bypass the gate")

	_, err = h.engine.Complete(h.ctx, userID, &leapmuxv1.WorkflowAgentCompleteRequest{Auth: h.leadAuth, Summary: "done"})
	assert.ErrorIs(t, err, workflow.ErrConflict, "the Lead cannot finish around a pending approval")

	var gate *leapmuxv1.WorkflowApproval
	for _, a := range h.detail().GetApprovals() {
		if a.GetKind() == leapmuxv1.WorkflowApprovalKind_WORKFLOW_APPROVAL_KIND_TASK_START {
			gate = a
		}
	}
	require.NotNil(t, gate)
	_, err = h.engine.DecideApproval(h.ctx, userID, &leapmuxv1.DecideWorkflowApprovalRequest{RunId: h.runID, ApprovalId: gate.GetId(), Approve: true})
	require.NoError(t, err)
	h.pass()
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, h.task("Planned merge").GetStatus())
}

func TestRestartAdoptsAnAgentOpenedBeforeTheCrash(t *testing.T) {
	h := newHarness(t)
	h.delegate("builder", "Implement")
	h.pass()
	require.Len(t, h.platform.openCalls(), 1)

	// The coordinator crashed after the hosting worker opened the agent but
	// before the slot recorded it: put the slot back to STARTING.
	_, err := h.db.ExecContext(h.ctx, `UPDATE workflow_run_agents SET status = ?, agent_id = '' WHERE role_key = 'builder'`,
		int32(leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_STARTING))
	require.NoError(t, err)

	restarted := newEngine(t, h.db, h.platform)
	restarted.Pass(h.ctx, true)
	assert.Len(t, h.platform.openCalls(), 1, "recovery adopts the agent instead of opening a second one")
	var slot *leapmuxv1.WorkflowRunAgent
	for _, a := range h.detail().GetAgents() {
		if a.GetRoleKey() == "builder" {
			slot = a
		}
	}
	require.NotNil(t, slot)
	assert.Equal(t, leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE, slot.GetStatus())
	assert.NotEmpty(t, slot.GetAgentId())
}

func TestUndeliveredMessagesAreResentOnceAfterAnOutage(t *testing.T) {
	h := newHarness(t)
	h.platform.enqueueErr = errors.New("connection reset")
	h.pass()
	assert.Empty(t, h.platform.messagesTo(h.leadID))
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, h.detail().GetRun().GetStatus())

	h.platform.enqueueErr = nil
	restarted := newEngine(t, h.db, h.platform)
	restarted.Pass(h.ctx, true)
	restarted.Pass(h.ctx, true)
	assert.Len(t, h.platform.messagesTo(h.leadID), 1, "the briefing survives a restart and arrives once")
}

func TestLostAgentRequeuesItsTaskOnAFreshAgent(t *testing.T) {
	h := newHarness(t)
	h.delegate("builder", "Implement")
	h.pass()
	_, agentID := h.authOf(builderPC)

	h.platform.closeAgent(builderPC, agentID)
	h.pass()
	got := h.task("Implement")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, got.GetStatus(), "re-dispatched in the same pass")
	assert.Equal(t, uint32(2), got.GetAttempt())
	calls := h.platform.openCalls()
	require.Len(t, calls, 2, "a fresh agent replaces the lost one")
	assert.Equal(t, builderPC, calls[1].workerID, "still on the pinned computer")
}

func TestUnreachableComputerMarksItsAgentAndRecovers(t *testing.T) {
	h := newHarness(t)
	h.delegate("builder", "Implement")
	h.pass()
	h.platform.setOnline(builderPC, false)
	h.pass()
	agents := h.detail().GetAgents()
	var builder *leapmuxv1.WorkflowRunAgent
	for _, a := range agents {
		if a.GetRoleKey() == "builder" {
			builder = a
		}
	}
	require.NotNil(t, builder)
	assert.Equal(t, leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_UNREACHABLE, builder.GetStatus())
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, h.task("Implement").GetStatus(),
		"an unreachable agent may still be working, so its task is not re-run")

	h.platform.setOnline(builderPC, true)
	h.pass()
	for _, a := range h.detail().GetAgents() {
		if a.GetRoleKey() == "builder" {
			assert.Equal(t, leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_ACTIVE, a.GetStatus())
		}
	}
	assert.Len(t, h.platform.openCalls(), 1)
}

func TestLostLeadBlocksTheRun(t *testing.T) {
	h := newHarness(t)
	h.platform.closeAgent(coordinator, h.leadID)
	h.pass()
	run := h.detail().GetRun()
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_BLOCKED, run.GetStatus())
	_, err := h.engine.ControlRun(h.ctx, userID, h.runID, leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_RESUME)
	assert.ErrorIs(t, err, workflow.ErrConflict)
}

func TestPauseHoldsDispatchAndCancelStopsTheAgents(t *testing.T) {
	h := newHarness(t)
	_, err := h.engine.ControlRun(h.ctx, userID, h.runID, leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_PAUSE)
	require.NoError(t, err)
	h.delegate("builder", "Implement")
	h.pass()
	assert.Empty(t, h.platform.openCalls(), "a paused run starts nothing")
	assert.Empty(t, h.platform.messagesTo(h.leadID), "a paused run sends nothing")

	_, err = h.engine.ControlRun(h.ctx, userID, h.runID, leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_RESUME)
	require.NoError(t, err)
	h.pass()
	require.Len(t, h.platform.openCalls(), 1)
	_, agentID := h.authOf(builderPC)

	_, err = h.engine.ControlRun(h.ctx, userID, h.runID, leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_CANCEL)
	require.NoError(t, err)
	assert.Contains(t, h.platform.interrupted, agentID)
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_CANCELLED, h.task("Implement").GetStatus())
	_, err = h.engine.Delegate(h.ctx, userID, &leapmuxv1.WorkflowAgentDelegateRequest{Auth: h.leadAuth, RoleKey: "builder", Title: "More", Instructions: "x"})
	assert.ErrorIs(t, err, workflow.ErrConflict, "a cancelled run takes no more work")
}

func TestReassignMovesATaskToTheChosenComputer(t *testing.T) {
	h := newHarness(t)
	h.platform.setOnline(builderPC, false)
	task := h.delegate("builder", "Implement")
	h.pass()
	_, err := h.engine.ControlTask(h.ctx, userID, &leapmuxv1.ControlWorkflowTaskRequest{
		RunId: h.runID, TaskId: task.GetId(), Action: leapmuxv1.WorkflowTaskAction_WORKFLOW_TASK_ACTION_REASSIGN, WorkerId: macPC,
	})
	require.NoError(t, err)
	h.pass()
	calls := h.platform.openCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, macPC, calls[0].workerID, "the user's reassignment is the authority to move")

	_, err = h.engine.ControlTask(h.ctx, userID, &leapmuxv1.ControlWorkflowTaskRequest{
		RunId: h.runID, TaskId: task.GetId(), Action: leapmuxv1.WorkflowTaskAction_WORKFLOW_TASK_ACTION_REASSIGN, WorkerId: "w-nobody",
	})
	assert.ErrorIs(t, err, workflow.ErrInvalid)
}

func TestCompleteRequiresEveryTaskSettled(t *testing.T) {
	h := newHarness(t)
	task := h.delegate("ios", "Simulator tests")
	h.pass()
	_, err := h.engine.Complete(h.ctx, userID, &leapmuxv1.WorkflowAgentCompleteRequest{Auth: h.leadAuth, Summary: "done"})
	require.ErrorIs(t, err, workflow.ErrConflict)

	auth, _ := h.authOf(macPC)
	_, err = h.engine.Report(h.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: task.GetId(), Result: &leapmuxv1.WorkflowTaskResult{
		Outcome: leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED, Summary: "Green.", TestsRan: true, TestsPassed: true, TestSummary: "42 passed",
	}})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, h.task("Simulator tests").GetStatus(), "an unreviewed role succeeds on a valid report")

	for _, title := range []string{"Planned implement", "Planned merge"} {
		_, err := h.engine.ControlTask(h.ctx, userID, &leapmuxv1.ControlWorkflowTaskRequest{
			RunId: h.runID, TaskId: h.task(title).GetId(), Action: leapmuxv1.WorkflowTaskAction_WORKFLOW_TASK_ACTION_CANCEL,
		})
		require.NoError(t, err)
	}
	run, err := h.engine.Complete(h.ctx, userID, &leapmuxv1.WorkflowAgentCompleteRequest{Auth: h.leadAuth, Summary: "Shipped."})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED, run.GetStatus())
}

func TestCreateRunRefusesALeadPinnedElsewhere(t *testing.T) {
	db := openDB(t)
	e := newEngine(t, db, newFakePlatform())
	tpl := team()
	tpl.Roles[0].Agent.Placement = &leapmuxv1.WorkerPlacement{
		Strategy: leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER, WorkerId: macPC,
	}
	_, err := e.CreateRun(t.Context(), userID, &leapmuxv1.CreateWorkflowRunRequest{
		Template: tpl, Objective: "x", WorkingDir: "/repo", WorkspaceId: "ws",
	})
	assert.ErrorIs(t, err, workflow.ErrConflict, "a pinned Lead is never coordinated from another computer")
}

func TestAttachLeadRefusesAnAgentWithoutTheRunBinding(t *testing.T) {
	db := openDB(t)
	e := newEngine(t, db, newFakePlatform())
	resp, err := e.CreateRun(t.Context(), userID, &leapmuxv1.CreateWorkflowRunRequest{
		Template: team(), Objective: "x", WorkingDir: "/repo", WorkspaceId: "ws",
	})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO agents (id) VALUES ('stranger')`)
	require.NoError(t, err)
	_, err = e.AttachLead(t.Context(), userID, resp.GetRun().GetId(), "stranger")
	assert.ErrorIs(t, err, workflow.ErrDenied)
}

func TestAgentsAreUnconfinedUnlessTheTemplateAsks(t *testing.T) {
	h := newHarness(t)
	h.delegate("builder", "Implement")
	h.pass()
	calls := h.platform.openCalls()
	require.Len(t, calls, 1)
	assert.False(t, calls[0].req.GetWorkflow().GetConfine(), "a workflow agent keeps its full control socket by default")
	var helperHint bool
	for _, m := range h.platform.messagesTo(h.leadID) {
		helperHint = helperHint || strings.Contains(m, "open helper agents")
	}
	assert.True(t, helperHint, "the Lead is told it may open helper agents")

	db := openDB(t)
	e := newEngine(t, db, newFakePlatform())
	tpl := team()
	tpl.ConfineAgents = true
	resp, err := e.CreateRun(t.Context(), userID, &leapmuxv1.CreateWorkflowRunRequest{
		Template: tpl, Objective: "x", WorkingDir: "/repo", WorkspaceId: "ws",
	})
	require.NoError(t, err)
	assert.True(t, resp.GetLeadBinding().GetConfine(), "a confining template confines its Lead too")
}
