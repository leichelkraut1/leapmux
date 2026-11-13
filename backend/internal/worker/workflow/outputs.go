package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// maxInputBytes bounds the inputs one dispatch carries, well inside the
// channel's message limit.
const maxInputBytes = 1 << 20

// Executor runs an executor step in-process on the coordinator. It must be
// idempotent per task: a coordinator that restarts mid-run calls it again for
// the same task and attempt, and it must then return the same result rather
// than act twice.
type Executor interface {
	Execute(ctx context.Context, req ExecutorRequest) (*ExecutorResult, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, req ExecutorRequest) (*ExecutorResult, error)

func (f ExecutorFunc) Execute(ctx context.Context, req ExecutorRequest) (*ExecutorResult, error) {
	return f(ctx, req)
}

// ExecutorRequest is what an executor step receives. Executors are trusted
// code, so beside the declared inputs they see every frozen output of the run
// under its real step.
type ExecutorRequest struct {
	UserID string
	Run    *leapmuxv1.WorkflowRun
	Task   *leapmuxv1.WorkflowTask
	Step   *leapmuxv1.WorkflowStep
	Inputs []*leapmuxv1.WorkflowTaskInput
	Frozen []*leapmuxv1.WorkflowFrozenOutput
}

// ExecutorResult is an executor's report. StructuredOutput is validated and
// frozen exactly like an agent's.
type ExecutorResult struct {
	Summary          string
	StructuredOutput string
	Evidence         []*leapmuxv1.WorkflowEvidence
}

// RegisterExecutor makes name available to executor steps. Call it before Run.
func (e *Engine) RegisterExecutor(name string, x Executor) {
	e.executorsMu.Lock()
	defer e.executorsMu.Unlock()
	e.executors[name] = x
}

func (e *Engine) executor(name string) Executor {
	e.executorsMu.RLock()
	defer e.executorsMu.RUnlock()
	return e.executors[name]
}

// checkExecutors refuses a template that names an executor this coordinator
// does not have, at launch rather than when the step comes up.
func (e *Engine) checkExecutors(t *leapmuxv1.WorkflowTemplate) error {
	for _, s := range t.GetSteps() {
		if s.GetExecutor() != "" && e.executor(s.GetExecutor()) == nil {
			return invalidf("step %q uses executor %q, which this computer does not provide", s.GetKey(), s.GetExecutor())
		}
	}
	return nil
}

func stepOf(run *leapmuxv1.WorkflowRun, key string) *leapmuxv1.WorkflowStep {
	if key == "" {
		return nil
	}
	for _, s := range run.GetTemplate().GetSteps() {
		if s.GetKey() == key {
			return s
		}
	}
	return nil
}

// --- Frozen outputs ---

const frozenColumns = `run_id, task_id, step_key, role_key, executor, sha256, canonical_json, attempt, slot_id, frozen_at_ms`

func scanFrozen(row interface{ Scan(...any) error }) (*leapmuxv1.WorkflowFrozenOutput, error) {
	f := &leapmuxv1.WorkflowFrozenOutput{}
	err := row.Scan(&f.RunId, &f.TaskId, &f.StepKey, &f.RoleKey, &f.Executor, &f.Sha256, &f.CanonicalJson, &f.Attempt, &f.SlotId, &f.FrozenAtMs)
	return f, err
}

// insertFrozen stores an output once. Storing the same output again is a
// no-op; storing a different one for the same task is refused, because the
// row is the record of what the task submitted.
func insertFrozen(ctx context.Context, q querier, f *leapmuxv1.WorkflowFrozenOutput) error {
	if _, err := q.ExecContext(ctx, `INSERT INTO workflow_frozen_outputs (`+frozenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (run_id, task_id) DO NOTHING`,
		f.GetRunId(), f.GetTaskId(), f.GetStepKey(), f.GetRoleKey(), f.GetExecutor(), f.GetSha256(), f.GetCanonicalJson(), f.GetAttempt(), f.GetSlotId(), f.GetFrozenAtMs()); err != nil {
		return err
	}
	stored, err := scanFrozen(q.QueryRowContext(ctx, `SELECT `+frozenColumns+` FROM workflow_frozen_outputs WHERE run_id = ? AND task_id = ?`, f.GetRunId(), f.GetTaskId()))
	if err != nil {
		return err
	}
	if stored.GetSha256() != f.GetSha256() {
		return conflictf("task %s already froze a different output (%s)", f.GetTaskId(), stored.GetSha256())
	}
	return nil
}

func listFrozen(ctx context.Context, q querier, runID string) ([]*leapmuxv1.WorkflowFrozenOutput, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+frozenColumns+` FROM workflow_frozen_outputs WHERE run_id = ? ORDER BY frozen_at_ms, task_id`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowFrozenOutput
	for rows.Next() {
		f, err := scanFrozen(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func runHasFrozen(ctx context.Context, q querier, runID string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM workflow_frozen_outputs WHERE run_id = ? LIMIT 1`, runID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// VerifyFrozen recomputes the hash of every frozen output of a run and
// returns the tasks whose stored text no longer matches its stored hash.
func (e *Engine) VerifyFrozen(ctx context.Context, runID string) ([]string, error) {
	frozen, err := listFrozen(ctx, e.store.db, runID)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, f := range frozen {
		if HashHex(f.GetCanonicalJson()) != f.GetSha256() {
			bad = append(bad, f.GetTaskId())
		}
	}
	return bad, nil
}

// freezeResult stores a succeeded task's structured output when its step asks.
func (e *Engine) freezeResult(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask) error {
	step := stepOf(rs.run, t.GetStepKey())
	if step == nil || !step.GetFreezeOutput() {
		return nil
	}
	canonical, sum, err := CanonicalJSON(t.GetResult().GetStructuredOutput())
	if err != nil {
		return err
	}
	return insertFrozen(ctx, q, &leapmuxv1.WorkflowFrozenOutput{
		RunId: rs.run.GetId(), TaskId: t.GetId(), StepKey: t.GetStepKey(), RoleKey: t.GetRoleKey(), Executor: step.GetExecutor(),
		Sha256: sum, CanonicalJson: canonical, Attempt: max(t.GetAttempt(), 1), SlotId: t.GetSlotId(), FrozenAtMs: e.nowMs(),
	})
}

// --- Input labels ---

func listInputLabels(ctx context.Context, q querier, runID string) ([]*leapmuxv1.WorkflowInputLabel, error) {
	rows, err := q.QueryContext(ctx, `SELECT step_key, label, source_step_key, source_task_id, sha256 FROM workflow_input_labels WHERE run_id = ? ORDER BY step_key, label`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowInputLabel
	for rows.Next() {
		l := &leapmuxv1.WorkflowInputLabel{}
		if err := rows.Scan(&l.StepKey, &l.Label, &l.SourceStepKey, &l.SourceTaskId, &l.Sha256); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func insertInputLabel(ctx context.Context, q querier, runID string, l *leapmuxv1.WorkflowInputLabel) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workflow_input_labels (run_id, step_key, label, source_step_key, source_task_id, sha256) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, l.GetStepKey(), l.GetLabel(), l.GetSourceStepKey(), l.GetSourceTaskId(), l.GetSha256())
	return err
}

// submissionLabel names the i-th anonymized input: A..Z, then AA, AB, ...
func submissionLabel(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return "Submission " + name
}

type inputSource struct {
	in     *leapmuxv1.WorkflowStepInput
	step   *leapmuxv1.WorkflowStep
	task   *leapmuxv1.WorkflowTask
	frozen *leapmuxv1.WorkflowFrozenOutput
}

// composeInputs gathers a task's declared inputs from the run's frozen
// outputs. Anonymized inputs take labels recorded at their first dispatch,
// assigned in an order seeded by the run and step, so a retry shows the same
// submission under the same label and no label says where it came from.
func (e *Engine) composeInputs(ctx context.Context, q querier, rs *runState, t *leapmuxv1.WorkflowTask) ([]*leapmuxv1.WorkflowTaskInput, error) {
	step := stepOf(rs.run, t.GetStepKey())
	if step == nil || len(step.GetInputs()) == 0 {
		return nil, nil
	}
	frozen, err := listFrozen(ctx, q, rs.run.GetId())
	if err != nil {
		return nil, err
	}
	byTask := map[string]*leapmuxv1.WorkflowFrozenOutput{}
	for _, f := range frozen {
		byTask[f.GetTaskId()] = f
	}
	var plain, anon []inputSource
	for _, in := range step.GetInputs() {
		src := inputSource{in: in, step: stepOf(rs.run, in.GetStepKey())}
		for _, cand := range rs.tasks {
			if cand.GetStepKey() == in.GetStepKey() && cand.GetStatus() == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_SUCCEEDED {
				src.task = cand
			}
		}
		if src.task != nil {
			src.frozen = byTask[src.task.GetId()]
		}
		if src.frozen == nil {
			return nil, conflictf("input %q has no frozen output yet", in.GetStepKey())
		}
		if in.GetAnonymize() {
			anon = append(anon, src)
		} else {
			plain = append(plain, src)
		}
	}

	var out []*leapmuxv1.WorkflowTaskInput
	total := 0
	add := func(label string, src inputSource) error {
		doc, err := RedactJSON(src.frozen.GetCanonicalJson(), src.in.GetRedactFields())
		if err != nil {
			return err
		}
		total += len(doc)
		if total > maxInputBytes {
			return conflictf("the inputs of step %q exceed %d bytes", step.GetKey(), maxInputBytes)
		}
		out = append(out, &leapmuxv1.WorkflowTaskInput{Label: label, Sha256: HashHex(doc), Json: doc})
		return nil
	}
	for _, src := range plain {
		label := strings.TrimSpace(src.in.GetLabel())
		if label == "" {
			label = src.step.GetTitle()
		}
		if err := add(label, src); err != nil {
			return nil, err
		}
	}
	if len(anon) == 0 {
		return out, nil
	}

	labels, err := listInputLabels(ctx, q, rs.run.GetId())
	if err != nil {
		return nil, err
	}
	known := map[string]string{} // source task -> label
	for _, l := range labels {
		if l.GetStepKey() == step.GetKey() {
			known[l.GetSourceTaskId()] = l.GetLabel()
		}
	}
	missing := false
	for _, src := range anon {
		if known[src.task.GetId()] == "" {
			missing = true
		}
	}
	if missing {
		if len(known) > 0 {
			return nil, conflictf("step %q already labelled a different set of submissions", step.GetKey())
		}
		order := slices.Clone(anon)
		seed := func(s inputSource) string {
			return HashHex(rs.run.GetId() + "/" + step.GetKey() + "/" + s.task.GetId())
		}
		slices.SortFunc(order, func(a, b inputSource) int { return strings.Compare(seed(a), seed(b)) })
		for i, src := range order {
			l := &leapmuxv1.WorkflowInputLabel{
				StepKey: step.GetKey(), Label: submissionLabel(i), SourceStepKey: src.in.GetStepKey(),
				SourceTaskId: src.task.GetId(), Sha256: src.frozen.GetSha256(),
			}
			if err := insertInputLabel(ctx, q, rs.run.GetId(), l); err != nil {
				return nil, err
			}
			known[src.task.GetId()] = l.GetLabel()
		}
	}
	slices.SortFunc(anon, func(a, b inputSource) int { return strings.Compare(known[a.task.GetId()], known[b.task.GetId()]) })
	for _, src := range anon {
		if err := add(known[src.task.GetId()], src); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func inputsText(inputs []*leapmuxv1.WorkflowTaskInput) string {
	if len(inputs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Inputs\n\nThese are frozen: they are exactly what was submitted, and they will not change.\n\n")
	for _, in := range inputs {
		fmt.Fprintf(&b, "### %s\n\nsha256 `%s`\n\n```json\n%s\n```\n\n", in.GetLabel(), in.GetSha256(), in.GetJson())
	}
	return b.String()
}
