package workflow

import (
	"context"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// startExecutor runs an executor task in the background, once per process.
// A task stays RUNNING while its executor works, so a coordinator that dies
// mid-run finds it RUNNING at its next pass and runs it again: that is why
// an executor must be idempotent per task.
func (e *Engine) startExecutor(ctx context.Context, ref runRef, taskID string) {
	if _, busy := e.executing.LoadOrStore(taskID, struct{}{}); busy {
		return
	}
	e.execWG.Add(1)
	go func() {
		defer e.execWG.Done()
		defer e.executing.Delete(taskID)
		e.runExecutor(ctx, ref, taskID)
		e.Wake()
	}()
}

func (e *Engine) runExecutor(ctx context.Context, ref runRef, taskID string) {
	var req ExecutorRequest
	var attempt uint32
	if err := e.mutate(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, ref.userID, ref.runID)
		if err != nil {
			return err
		}
		t := rs.task(taskID)
		if t == nil || t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING {
			return ErrNotFound
		}
		inputs, err := e.composeInputs(ctx, q, rs, t)
		if err != nil {
			return err
		}
		frozen, err := listFrozen(ctx, q, rs.run.GetId())
		if err != nil {
			return err
		}
		labels, err := listInputLabels(ctx, q, rs.run.GetId())
		if err != nil {
			return err
		}
		attempt = t.GetAttempt()
		req = ExecutorRequest{UserID: ref.userID, Run: rs.run, Task: t, Step: stepOf(rs.run, t.GetStepKey()), Inputs: inputs, Frozen: frozen, Labels: labels}
		return nil
	}); err != nil {
		if !errors.Is(err, ErrNotFound) {
			e.log.Warn("load workflow executor task", "task", taskID, "error", err)
		}
		return
	}
	name := req.Step.GetExecutor()
	x := e.executor(name)
	var res *ExecutorResult
	var runErr error
	if x == nil {
		runErr = &PermanentError{Err: fmt.Errorf("this computer has no executor %q", name)}
	} else {
		res, runErr = x.Execute(ctx, req)
	}
	if ctx.Err() != nil {
		// Shutting down: the task stays RUNNING and runs again at restart,
		// without spending an attempt on our own exit.
		return
	}
	if err := e.mutate(ctx, func(q querier) error {
		rs, err := loadRun(ctx, q, ref.userID, ref.runID)
		if err != nil {
			return err
		}
		t := rs.task(taskID)
		if t == nil || t.GetStatus() != leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING || t.GetAttempt() != attempt {
			return nil // cancelled or re-queued while it ran
		}
		if runErr != nil {
			return e.executorFailed(ctx, q, rs, t, name, runErr)
		}
		result := &leapmuxv1.WorkflowTaskResult{
			Outcome: leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED, Summary: res.Summary,
			StructuredOutput: res.StructuredOutput, Evidence: res.Evidence, ReportedAtMs: e.nowMs(),
		}
		if result.GetSummary() == "" {
			result.Summary = name + " completed"
		}
		if req.Step.GetFreezeOutput() || result.GetStructuredOutput() != "" {
			canonical, sum, err := ValidateOutput(req.Step.GetOutputSchema(), result.GetStructuredOutput())
			if err != nil {
				return e.executorFailed(ctx, q, rs, t, name, err)
			}
			result.StructuredOutput, result.OutputSha256 = canonical, sum
		}
		t.Result = result
		if err := e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_RESULT, "engine",
			"%s reported completed: %s", name, firstLine(result.GetSummary())); err != nil {
			return err
		}
		if err := e.acceptResult(ctx, q, rs, t, "engine"); err != nil {
			return err
		}
		return e.settleRunStatus(ctx, q, rs)
	}); err != nil {
		e.log.Warn("record workflow executor result", "task", taskID, "error", err)
	}
}

// executorFailed blocks a task whose executor can never succeed and retries
// one that may.
func (e *Engine) executorFailed(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask, name string, cause error) error {
	if err := e.event(ctx, q, rs, t.GetId(), leapmuxv1.WorkflowEventKind_WORKFLOW_EVENT_KIND_ERROR, "engine", "%s failed: %v", name, cause); err != nil {
		return err
	}
	var permanent *PermanentError
	if errors.As(cause, &permanent) {
		return e.setTaskStatus(ctx, q, rs, t, leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_BLOCKED, "engine", cause.Error())
	}
	return e.retryOrEscalate(ctx, q, rs, t, "engine", fmt.Sprintf("%s failed: %v", name, cause))
}
