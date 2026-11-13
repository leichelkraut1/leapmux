package workflow_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

const pickSchema = `{
  "type": "object",
  "required": ["pick", "confidence"],
  "properties": {
    "pick": {"type": "string", "minLength": 1},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1},
    "analyst": {"type": "string"}
  },
  "additionalProperties": false
}`

const scoreSchema = `{"type": "object", "required": ["scores"], "properties": {"scores": {"type": "object"}}}`

const contrarianSecret = "Fade the crowded long; the market overreacts."

// blindTeam is an engine-coordinated experiment: an executor freezes shared
// data, two blind analysts work on it at once, and an evaluator scores their
// frozen, anonymized picks.
func blindTeam() *leapmuxv1.WorkflowTemplate {
	isolated := &leapmuxv1.WorkflowRoleIsolation{FreshAgentPerTask: true, PrivateWorkingDir: true, Blind: true}
	role := func(key, name, instructions string) *leapmuxv1.WorkflowRole {
		return &leapmuxv1.WorkflowRole{
			Key: key, Name: name, Isolation: isolated,
			Agent: &leapmuxv1.AgentProfile{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Instructions: instructions},
		}
	}
	return &leapmuxv1.WorkflowTemplate{
		Name:         "Blind picks",
		Coordination: leapmuxv1.WorkflowCoordination_WORKFLOW_COORDINATION_ENGINE,
		Roles: []*leapmuxv1.WorkflowRole{
			role("blind", "Analyst", "Pick neutrally."),
			role("contrarian", "Contrarian", contrarianSecret),
			role("judge", "Evaluator", "Score every submission by the same rules."),
		},
		Steps: []*leapmuxv1.WorkflowStep{
			{Key: "data", Title: "Data", Executor: "test.data", FreezeOutput: true},
			{Key: "pick-a", Title: "Blind pick", RoleKey: "blind", DependsOn: []string{"data"}, OutputSchema: pickSchema, FreezeOutput: true,
				Inputs: []*leapmuxv1.WorkflowStepInput{{StepKey: "data", Label: "Market data"}}},
			{Key: "pick-b", Title: "Contrarian pick", RoleKey: "contrarian", DependsOn: []string{"data"}, OutputSchema: pickSchema, FreezeOutput: true,
				Inputs: []*leapmuxv1.WorkflowStepInput{{StepKey: "data", Label: "Market data"}}},
			{Key: "judge", Title: "Score", RoleKey: "judge", DependsOn: []string{"pick-a", "pick-b"}, OutputSchema: scoreSchema, FreezeOutput: true,
				Inputs: []*leapmuxv1.WorkflowStepInput{
					{StepKey: "pick-a", Anonymize: true, RedactFields: []string{"analyst"}},
					{StepKey: "pick-b", Anonymize: true, RedactFields: []string{"analyst"}},
				}},
		},
		MaxParallelTasks: 3,
	}
}

type countingExecutor struct {
	calls atomic.Int32
	fail  atomic.Pointer[error]
}

func (x *countingExecutor) Execute(_ context.Context, req workflow.ExecutorRequest) (*workflow.ExecutorResult, error) {
	x.calls.Add(1)
	if err := x.fail.Load(); err != nil {
		return nil, *err
	}
	return &workflow.ExecutorResult{Summary: "Collected", StructuredOutput: `{"symbol":"XYZ","price":101.5,"task":"` + req.Task.GetId() + `"}`}, nil
}

type blindHarness struct {
	*harness
	data *countingExecutor
}

func newBlindHarness(t *testing.T) *blindHarness {
	t.Helper()
	h := &harness{t: t, ctx: t.Context(), db: openDB(t), platform: newFakePlatform()}
	h.platform.addWorker(coordinator, "Home PC", true, "os:linux")
	h.engine = newEngine(t, h.db, h.platform)
	b := &blindHarness{harness: h, data: &countingExecutor{}}
	h.engine.RegisterExecutor("test.data", b.data)
	resp, err := h.engine.CreateRun(h.ctx, userID, &leapmuxv1.CreateWorkflowRunRequest{
		Template: blindTeam(), Objective: "Hypothesis: XYZ is overbought", WorkingDir: "/repo", WorkspaceId: "ws-1",
	})
	require.NoError(t, err)
	h.runID = resp.GetRun().GetId()
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, resp.GetRun().GetStatus())
	assert.Nil(t, resp.GetLeadBinding(), "an engine-coordinated run has no Lead to open")
	return b
}

// settle runs passes until the executors have finished and been recorded.
func (b *blindHarness) settle() {
	b.pass()
	b.engine.WaitExecutors()
	b.pass()
}

func (b *blindHarness) open(roleKey string) (openCall, *leapmuxv1.WorkflowAgentAuth, string) {
	b.t.Helper()
	var found []openCall
	for _, c := range b.platform.openCalls() {
		if c.req.GetWorkflow().GetRoleKey() == roleKey {
			found = append(found, c)
		}
	}
	require.NotEmpty(b.t, found, "no agent opened for %s", roleKey)
	c := found[len(found)-1]
	for _, a := range b.detail().GetAgents() {
		if a.GetSlotId() == c.req.GetWorkflow().GetSlotId() {
			return c, &leapmuxv1.WorkflowAgentAuth{RunId: b.runID, Token: c.req.GetWorkflow().GetToken()}, a.GetAgentId()
		}
	}
	b.t.Fatalf("no slot for %s", roleKey)
	return openCall{}, nil, ""
}

func (b *blindHarness) report(roleKey, title, output string) error {
	_, auth, _ := b.open(roleKey)
	_, err := b.engine.Report(b.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{
		Auth: auth, TaskId: b.task(title).GetId(),
		Result: &leapmuxv1.WorkflowTaskResult{
			Outcome: leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED, Summary: "Done.", StructuredOutput: output,
		},
	})
	return err
}

func TestEngineRunFreezesTheExecutorOutputThenStartsBothAnalystsAtOnce(t *testing.T) {
	b := newBlindHarness(t)
	b.settle()

	assert.Equal(t, int32(1), b.data.calls.Load())
	data := b.task("Data")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, data.GetStatus())
	frozen := b.detail().GetFrozenOutputs()
	require.Len(t, frozen, 1)
	assert.Equal(t, workflow.HashHex(frozen[0].GetCanonicalJson()), frozen[0].GetSha256())
	assert.Equal(t, frozen[0].GetSha256(), data.GetResult().GetOutputSha256())

	b.pass()
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, b.task("Blind pick").GetStatus())
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, b.task("Contrarian pick").GetStatus())
	assert.Len(t, b.platform.openCalls(), 2, "the two analysts are opened in the same pass")
}

func TestBlindAnalystsAreConfinedPrivateAndToldNothingOfEachOther(t *testing.T) {
	b := newBlindHarness(t)
	b.settle()
	b.pass()

	blindCall, blindAuth, blindAgent := b.open("blind")
	contraCall, _, contraAgent := b.open("contrarian")
	for _, c := range []openCall{blindCall, contraCall} {
		assert.True(t, c.req.GetWorkflow().GetConfine(), "a blind role is always confined")
		assert.True(t, c.req.GetWorkflow().GetPrivateDir())
		assert.False(t, c.req.GetCreateWorktree())
	}
	assert.NotEqual(t, blindCall.req.GetWorkflow().GetSlotId(), contraCall.req.GetWorkflow().GetSlotId())

	blindText := strings.Join(b.platform.messagesTo(blindAgent), "\n")
	assert.Contains(t, blindText, `"symbol":"XYZ"`, "the analyst receives the frozen data")
	assert.Contains(t, blindText, "Pick neutrally.")
	for _, leak := range []string{contrarianSecret, "overbought", "Blind picks", "Contrarian"} {
		assert.NotContains(t, blindText, leak)
	}
	assert.Contains(t, strings.Join(b.platform.messagesTo(contraAgent), "\n"), contrarianSecret)

	ctx, err := b.engine.AgentContext(b.ctx, userID, blindAuth)
	require.NoError(t, err)
	assert.Empty(t, ctx.GetRun().GetObjective())
	assert.Nil(t, ctx.GetRun().GetTemplate())
	require.Len(t, ctx.GetRoles(), 1)
	assert.Equal(t, "blind", ctx.GetRoles()[0].GetRoleKey())
	assert.Len(t, ctx.GetAgents(), 1)
	require.Len(t, ctx.GetInputs(), 1)
	assert.Equal(t, "Market data", ctx.GetInputs()[0].GetLabel())
}

func TestStructuredOutputIsValidatedFrozenAndImmutable(t *testing.T) {
	b := newBlindHarness(t)
	b.settle()
	b.pass()

	err := b.report("blind", "Blind pick", "")
	assert.ErrorIs(t, err, workflow.ErrInvalid, "a completed report needs its structured output")
	err = b.report("blind", "Blind pick", `{"pick": "XYZ 110C", "confidence": 3}`)
	assert.ErrorIs(t, err, workflow.ErrInvalid, "the schema bounds confidence")
	require.NoError(t, b.report("blind", "Blind pick", `{ "confidence": 0.4, "pick": "XYZ 110C", "analyst": "blind" }`))

	pick := b.task("Blind pick")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, pick.GetStatus())
	assert.Equal(t, `{"analyst":"blind","confidence":0.4,"pick":"XYZ 110C"}`, pick.GetResult().GetStructuredOutput())
	var stored *leapmuxv1.WorkflowFrozenOutput
	for _, f := range b.detail().GetFrozenOutputs() {
		if f.GetTaskId() == pick.GetId() {
			stored = f
		}
	}
	require.NotNil(t, stored)
	assert.Equal(t, pick.GetResult().GetOutputSha256(), stored.GetSha256())
	assert.Equal(t, "blind", stored.GetRoleKey())

	_, err = b.db.ExecContext(b.ctx, `UPDATE workflow_frozen_outputs SET canonical_json = '{}' WHERE task_id = ?`, pick.GetId())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
	_, err = b.db.ExecContext(b.ctx, `DELETE FROM workflow_frozen_outputs WHERE task_id = ?`, pick.GetId())
	require.Error(t, err)
	bad, err := b.engine.VerifyFrozen(b.ctx, b.runID)
	require.NoError(t, err)
	assert.Empty(t, bad)

	_, err = b.engine.ControlRun(b.ctx, userID, b.runID, leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_CANCEL)
	require.NoError(t, err)
	_, err = b.engine.ControlRun(b.ctx, userID, b.runID, leapmuxv1.WorkflowRunAction_WORKFLOW_RUN_ACTION_DELETE)
	assert.ErrorIs(t, err, workflow.ErrConflict, "a run with frozen outputs is kept")
}

func TestEvaluatorScoresAnonymizedSubmissionsAndTheRunCompletes(t *testing.T) {
	b := newBlindHarness(t)
	b.settle()
	b.pass()
	require.NoError(t, b.report("blind", "Blind pick", `{"pick": "XYZ 110C", "confidence": 0.4, "analyst": "blind"}`))
	require.NoError(t, b.report("contrarian", "Contrarian pick", `{"pick": "XYZ 95P", "confidence": 0.3, "analyst": "contrarian"}`))

	for _, s := range b.detail().GetAgents() {
		assert.Equal(t, leapmuxv1.WorkflowAgentStatus_WORKFLOW_AGENT_STATUS_CLOSED, s.GetStatus(), "each analyst's session ends with its task")
	}
	b.pass()
	_, _, judge := b.open("judge")
	text := strings.Join(b.platform.messagesTo(judge), "\n")
	assert.Contains(t, text, "### Submission A")
	assert.Contains(t, text, "### Submission B")
	assert.Contains(t, text, "XYZ 110C")
	assert.Contains(t, text, "XYZ 95P")
	for _, leak := range []string{`"analyst"`, "Blind pick", "Contrarian pick", "pick-a", "pick-b", contrarianSecret} {
		assert.NotContains(t, text, leak)
	}

	labels := b.detail().GetInputLabels()
	require.Len(t, labels, 2)
	sources := map[string]string{}
	for _, l := range labels {
		assert.Equal(t, "judge", l.GetStepKey())
		sources[l.GetLabel()] = l.GetSourceStepKey()
	}
	assert.ElementsMatch(t, []string{"pick-a", "pick-b"}, []string{sources["Submission A"], sources["Submission B"]})

	require.NoError(t, b.report("judge", "Score", `{"scores": {"Submission A": 6, "Submission B": 7}}`))
	b.pass()
	d := b.detail()
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED, d.GetRun().GetStatus())
	assert.Len(t, d.GetFrozenOutputs(), 4)
}

func TestAnonymizedLabelsSurviveARetry(t *testing.T) {
	b := newBlindHarness(t)
	b.settle()
	b.pass()
	require.NoError(t, b.report("blind", "Blind pick", `{"pick": "A", "confidence": 0.1}`))
	require.NoError(t, b.report("contrarian", "Contrarian pick", `{"pick": "B", "confidence": 0.2}`))
	b.pass()
	before := b.detail().GetInputLabels()

	_, auth, judge := b.open("judge")
	_, err := b.engine.Report(b.ctx, userID, &leapmuxv1.WorkflowAgentReportRequest{
		Auth: auth, TaskId: b.task("Score").GetId(),
		Result: &leapmuxv1.WorkflowTaskResult{Outcome: leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_FAILED, Summary: "Lost my notes."},
	})
	require.NoError(t, err)
	b.pass()
	assert.Equal(t, uint32(2), b.task("Score").GetAttempt())
	assert.Equal(t, before, b.detail().GetInputLabels())
	var retry string
	for _, m := range b.platform.messagesTo(judge) {
		if strings.Contains(m, "attempt 2") {
			retry = m
		}
	}
	require.NotEmpty(t, retry, "the retry goes back to the same session")
	assert.Contains(t, retry, "### Submission A")
}

func TestExecutorRunsAgainAfterACoordinatorRestart(t *testing.T) {
	b := newBlindHarness(t)
	ctx, cancel := context.WithCancel(b.ctx)
	started := make(chan struct{})
	b.engine.RegisterExecutor("test.data", workflow.ExecutorFunc(func(ctx context.Context, _ workflow.ExecutorRequest) (*workflow.ExecutorResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	b.engine.Pass(ctx, true)
	<-started
	cancel() // the process dies mid-executor
	b.engine.WaitExecutors()
	data := b.task("Data")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING, data.GetStatus())
	assert.Equal(t, uint32(1), data.GetAttempt(), "dying does not spend an attempt")

	// A new process over the same database finds the task RUNNING and runs
	// its executor again rather than losing it.
	restarted := newEngine(t, b.db, b.platform)
	again := &countingExecutor{}
	restarted.RegisterExecutor("test.data", again)
	restarted.Pass(b.ctx, true)
	restarted.WaitExecutors()
	restarted.Pass(b.ctx, true)
	assert.Equal(t, int32(1), again.calls.Load())
	d, err := restarted.GetRun(b.ctx, userID, b.runID)
	require.NoError(t, err)
	for _, task := range d.GetTasks() {
		if task.GetTitle() == "Data" {
			assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, task.GetStatus())
			assert.Equal(t, uint32(1), task.GetAttempt())
		}
	}
}

func TestTransientExecutorFailureRetries(t *testing.T) {
	b := newBlindHarness(t)
	transient := errors.New("quote feed timed out")
	b.data.fail.Store(&transient)
	b.pass()
	b.engine.WaitExecutors()
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED, b.task("Data").GetStatus())
	b.data.fail.Store(nil)
	b.settle()
	data := b.task("Data")
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, data.GetStatus())
	assert.Equal(t, uint32(2), data.GetAttempt())
}

func TestPermanentExecutorFailureBlocksTheEngineRun(t *testing.T) {
	b := newBlindHarness(t)
	var permanent error = &workflow.PermanentError{Err: errors.New("no market data source is configured")}
	b.data.fail.Store(&permanent)
	b.settle()
	d := b.detail()
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, b.task("Data").GetStatus())
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_BLOCKED, d.GetRun().GetStatus())
	assert.Contains(t, d.GetRun().GetStatusDetail(), "no market data source")

	b.data.fail.Store(nil)
	_, err := b.engine.ControlTask(b.ctx, userID, &leapmuxv1.ControlWorkflowTaskRequest{
		RunId: b.runID, TaskId: b.task("Data").GetId(), Action: leapmuxv1.WorkflowTaskAction_WORKFLOW_TASK_ACTION_RETRY,
	})
	require.NoError(t, err)
	b.settle()
	assert.Equal(t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED, b.task("Data").GetStatus())
	assert.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_RUNNING, b.detail().GetRun().GetStatus())
}

func TestEngineTemplateValidation(t *testing.T) {
	cases := map[string]func(*leapmuxv1.WorkflowTemplate){
		"a Lead in an engine template": func(tp *leapmuxv1.WorkflowTemplate) { tp.Roles[0].IsLead = true },
		"an input that is not a dependency": func(tp *leapmuxv1.WorkflowTemplate) {
			tp.Steps[3].DependsOn = []string{"pick-a"}
		},
		"an input whose step does not freeze": func(tp *leapmuxv1.WorkflowTemplate) { tp.Steps[1].FreezeOutput = false },
		"freezing an agent step without a schema": func(tp *leapmuxv1.WorkflowTemplate) {
			tp.Steps[1].OutputSchema = ""
		},
		"a schema that does not compile": func(tp *leapmuxv1.WorkflowTemplate) { tp.Steps[1].OutputSchema = `{"type": 7}` },
		"a schema that reaches outside":  func(tp *leapmuxv1.WorkflowTemplate) { tp.Steps[1].OutputSchema = `{"$ref": "file:///etc/passwd"}` },
		"an executor step with a role":   func(tp *leapmuxv1.WorkflowTemplate) { tp.Steps[0].RoleKey = "blind" },
		"review in an engine template":   func(tp *leapmuxv1.WorkflowTemplate) { tp.Roles[0].ReviewRequired = true },
		"a private dir in a worktree": func(tp *leapmuxv1.WorkflowTemplate) {
			tp.Roles[0].GitIsolation = leapmuxv1.WorkflowGitIsolation_WORKFLOW_GIT_ISOLATION_WORKTREE
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tp := blindTeam()
			mutate(tp)
			assert.ErrorIs(t, workflow.NormalizeTemplate(tp), workflow.ErrInvalid)
		})
	}

	tp := blindTeam()
	require.NoError(t, workflow.NormalizeTemplate(tp))
	for _, s := range tp.GetSteps() {
		assert.True(t, s.GetAutoStart(), "every engine step starts by itself")
	}

	db := openDB(t)
	e := newEngine(t, db, newFakePlatform())
	_, err := e.CreateRun(t.Context(), userID, &leapmuxv1.CreateWorkflowRunRequest{
		Template: blindTeam(), Objective: "o", WorkingDir: "/repo", WorkspaceId: "ws",
	})
	assert.ErrorIs(t, err, workflow.ErrInvalid, "a run is refused when its executor is missing here")
}

func TestCanonicalJSON(t *testing.T) {
	a, sumA, err := workflow.CanonicalJSON(`{"b": [1, 2.50, {"y": true, "x": null}], "a": "<&>"}`)
	require.NoError(t, err)
	assert.Equal(t, `{"a":"<&>","b":[1,2.50,{"x":null,"y":true}]}`, a)
	_, sumB, err := workflow.CanonicalJSON("{\n  \"a\": \"<&>\", \"b\": [1, 2.50, {\"x\": null, \"y\": true}]\n}")
	require.NoError(t, err)
	assert.Equal(t, sumA, sumB)
	_, _, err = workflow.CanonicalJSON(`{"a": 1} {"b": 2}`)
	assert.ErrorIs(t, err, workflow.ErrInvalid)
}
