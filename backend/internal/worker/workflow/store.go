package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ErrNotFound reports a row that does not exist for this user.
var ErrNotFound = errors.New("not found")

// Store persists workflow state in the worker database. Every enum ordinal is
// bound as a parameter from its proto constant, so a renumber propagates.
type Store struct {
	db *sql.DB
}

// NewStore wraps the worker database.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// querier is the part of *sql.DB and *sql.Tx the store uses, so one method
// serves inside and outside a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) inTx(ctx context.Context, fn func(q querier) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func marshal(m proto.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// --- Library ---

func (s *Store) ListProfiles(ctx context.Context, userID string) ([]*leapmuxv1.AgentProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec FROM workflow_profiles WHERE user_id = ? ORDER BY name, id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.AgentProfile
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, err
		}
		p := &leapmuxv1.AgentProfile{}
		if err := proto.Unmarshal(spec, p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PutProfile(ctx context.Context, userID string, p *leapmuxv1.AgentProfile) error {
	spec, err := marshal(p)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO workflow_profiles (id, user_id, name, spec, updated_at_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, spec = excluded.spec, updated_at_ms = excluded.updated_at_ms
		WHERE workflow_profiles.user_id = excluded.user_id`,
		p.GetId(), userID, p.GetName(), spec, p.GetUpdatedAtMs())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProfile(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workflow_profiles WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListTemplates(ctx context.Context, userID string) ([]*leapmuxv1.WorkflowTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec FROM workflow_templates WHERE user_id = ? ORDER BY name, id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowTemplate
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, err
		}
		t := &leapmuxv1.WorkflowTemplate{}
		if err := proto.Unmarshal(spec, t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTemplate(ctx context.Context, userID, id string) (*leapmuxv1.WorkflowTemplate, error) {
	var spec []byte
	if err := s.db.QueryRowContext(ctx, `SELECT spec FROM workflow_templates WHERE id = ? AND user_id = ?`, id, userID).Scan(&spec); err != nil {
		return nil, notFound(err)
	}
	t := &leapmuxv1.WorkflowTemplate{}
	return t, proto.Unmarshal(spec, t)
}

// PutTemplate inserts a template at version 1 or replaces it at the next
// version, and stamps that version on t.
func (s *Store) PutTemplate(ctx context.Context, userID string, t *leapmuxv1.WorkflowTemplate) error {
	return s.inTx(ctx, func(q querier) error {
		var version uint32
		err := q.QueryRowContext(ctx, `SELECT version FROM workflow_templates WHERE id = ? AND user_id = ?`, t.GetId(), userID).Scan(&version)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			var owner string
			if q.QueryRowContext(ctx, `SELECT user_id FROM workflow_templates WHERE id = ?`, t.GetId()).Scan(&owner) == nil {
				return ErrNotFound
			}
			version = 0
		case err != nil:
			return err
		}
		t.Version = version + 1
		spec, err := marshal(t)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO workflow_templates (id, user_id, name, version, spec, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name, version = excluded.version, spec = excluded.spec, updated_at_ms = excluded.updated_at_ms`,
			t.GetId(), userID, t.GetName(), t.GetVersion(), spec, t.GetUpdatedAtMs())
		return err
	})
}

func (s *Store) DeleteTemplate(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workflow_templates WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Runs ---

const runColumns = `id, name, template_id, template_version, template, status, status_detail, objective, working_dir,
	base_branch, workspace_id, lead_slot_id, lead_agent_id, summary, created_at_ms, started_at_ms, completed_at_ms, updated_at_ms`

func scanRun(row interface{ Scan(...any) error }) (*leapmuxv1.WorkflowRun, error) {
	r := &leapmuxv1.WorkflowRun{}
	var template []byte
	var status int32
	if err := row.Scan(&r.Id, &r.Name, &r.TemplateId, &r.TemplateVersion, &template, &status, &r.StatusDetail, &r.Objective,
		&r.WorkingDir, &r.BaseBranch, &r.WorkspaceId, &r.LeadSlotId, &r.LeadAgentId, &r.Summary,
		&r.CreatedAtMs, &r.StartedAtMs, &r.CompletedAtMs, &r.UpdatedAtMs); err != nil {
		return nil, err
	}
	r.Status = leapmuxv1.WorkflowRunStatus(status)
	r.Template = &leapmuxv1.WorkflowTemplate{}
	return r, proto.Unmarshal(template, r.Template)
}

func insertRun(ctx context.Context, q querier, userID string, r *leapmuxv1.WorkflowRun) error {
	template, err := marshal(r.GetTemplate())
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO workflow_runs (id, user_id, name, template_id, template_version, template, status, status_detail,
		objective, working_dir, base_branch, workspace_id, lead_slot_id, lead_agent_id, summary, created_at_ms, started_at_ms, completed_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.GetId(), userID, r.GetName(), r.GetTemplateId(), r.GetTemplateVersion(), template, int32(r.GetStatus()), r.GetStatusDetail(),
		r.GetObjective(), r.GetWorkingDir(), r.GetBaseBranch(), r.GetWorkspaceId(), r.GetLeadSlotId(), r.GetLeadAgentId(), r.GetSummary(),
		r.GetCreatedAtMs(), r.GetStartedAtMs(), r.GetCompletedAtMs(), r.GetUpdatedAtMs())
	return err
}

// updateRun writes a run's mutable columns.
func updateRun(ctx context.Context, q querier, r *leapmuxv1.WorkflowRun) error {
	_, err := q.ExecContext(ctx, `UPDATE workflow_runs SET status = ?, status_detail = ?, lead_agent_id = ?, summary = ?,
		started_at_ms = ?, completed_at_ms = ?, updated_at_ms = ? WHERE id = ?`,
		int32(r.GetStatus()), r.GetStatusDetail(), r.GetLeadAgentId(), r.GetSummary(),
		r.GetStartedAtMs(), r.GetCompletedAtMs(), r.GetUpdatedAtMs(), r.GetId())
	return err
}

func getRun(ctx context.Context, q querier, userID, id string) (*leapmuxv1.WorkflowRun, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM workflow_runs WHERE id = ? AND user_id = ?`, id, userID))
	return r, notFound(err)
}

func listRuns(ctx context.Context, q querier, userID string) ([]*leapmuxv1.WorkflowRun, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+runColumns+` FROM workflow_runs WHERE user_id = ? ORDER BY created_at_ms DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// activeRunIDs lists the runs the engine still drives: every run that has not
// reached a final status.
func activeRunIDs(ctx context.Context, q querier) ([]runRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, user_id FROM workflow_runs WHERE status NOT IN (?, ?, ?) ORDER BY created_at_ms`,
		int32(leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_FAILED),
		int32(leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_CANCELLED),
		int32(leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []runRef
	for rows.Next() {
		var ref runRef
		if err := rows.Scan(&ref.runID, &ref.userID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

type runRef struct{ runID, userID string }

func deleteRun(ctx context.Context, q querier, id string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM workflow_runs WHERE id = ?`, id)
	return err
}

// --- Roles ---

func putRole(ctx context.Context, q querier, runID string, a *leapmuxv1.WorkflowRoleAssignment) error {
	role, err := marshal(a.GetRole())
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO workflow_run_roles (run_id, role_key, role, worker_id) VALUES (?, ?, ?, ?)
		ON CONFLICT (run_id, role_key) DO UPDATE SET role = excluded.role, worker_id = excluded.worker_id`,
		runID, a.GetRoleKey(), role, a.GetWorkerId())
	return err
}

func listRoles(ctx context.Context, q querier, runID string) ([]*leapmuxv1.WorkflowRoleAssignment, error) {
	rows, err := q.QueryContext(ctx, `SELECT role_key, role, worker_id FROM workflow_run_roles WHERE run_id = ? ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowRoleAssignment
	for rows.Next() {
		a := &leapmuxv1.WorkflowRoleAssignment{Role: &leapmuxv1.WorkflowRole{}}
		var role []byte
		if err := rows.Scan(&a.RoleKey, &role, &a.WorkerId); err != nil {
			return nil, err
		}
		if err := proto.Unmarshal(role, a.Role); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- Agents (slots) ---

const slotColumns = `slot_id, run_id, role_key, agent_id, worker_id, status, status_detail, current_task_id, created_at_ms, token`

func scanSlot(row interface{ Scan(...any) error }) (*leapmuxv1.WorkflowRunAgent, error) {
	a := &leapmuxv1.WorkflowRunAgent{}
	var status int32
	var token string
	if err := row.Scan(&a.SlotId, &a.RunId, &a.RoleKey, &a.AgentId, &a.WorkerId, &status, &a.StatusDetail, &a.CurrentTaskId, &a.CreatedAtMs, &token); err != nil {
		return nil, err
	}
	a.Status = leapmuxv1.WorkflowAgentStatus(status)
	return a, nil
}

func insertSlot(ctx context.Context, q querier, a *leapmuxv1.WorkflowRunAgent, token string, nowMs int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workflow_run_agents (slot_id, run_id, role_key, agent_id, worker_id, status, status_detail,
		token, current_task_id, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.GetSlotId(), a.GetRunId(), a.GetRoleKey(), a.GetAgentId(), a.GetWorkerId(), int32(a.GetStatus()), a.GetStatusDetail(),
		token, a.GetCurrentTaskId(), a.GetCreatedAtMs(), nowMs)
	return err
}

func updateSlot(ctx context.Context, q querier, a *leapmuxv1.WorkflowRunAgent, nowMs int64) error {
	_, err := q.ExecContext(ctx, `UPDATE workflow_run_agents SET agent_id = ?, worker_id = ?, status = ?, status_detail = ?,
		current_task_id = ?, updated_at_ms = ? WHERE slot_id = ?`,
		a.GetAgentId(), a.GetWorkerId(), int32(a.GetStatus()), a.GetStatusDetail(), a.GetCurrentTaskId(), nowMs, a.GetSlotId())
	return err
}

func listSlots(ctx context.Context, q querier, runID string) ([]*leapmuxv1.WorkflowRunAgent, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+slotColumns+` FROM workflow_run_agents WHERE run_id = ? ORDER BY created_at_ms, slot_id`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowRunAgent
	for rows.Next() {
		a, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func slotByToken(ctx context.Context, q querier, token string) (*leapmuxv1.WorkflowRunAgent, error) {
	a, err := scanSlot(q.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM workflow_run_agents WHERE token = ?`, token))
	return a, notFound(err)
}

// slotToken is the credential a slot's agent is opened with. The slot views
// the dashboard reads never carry it.
func slotToken(ctx context.Context, q querier, slotID string) (string, error) {
	var token string
	err := q.QueryRowContext(ctx, `SELECT token FROM workflow_run_agents WHERE slot_id = ?`, slotID).Scan(&token)
	return token, notFound(err)
}

// slotHasMessages reports whether the engine already wrote to a slot's agent,
// which decides whether its next message carries the role briefing.
func slotHasMessages(ctx context.Context, q querier, slotID string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_outbox WHERE slot_id = ?`, slotID).Scan(&n)
	return n > 0, err
}

// --- Tasks ---

const taskColumns = `id, run_id, step_key, role_key, title, instructions, status, status_detail, approval, attempt, max_attempts,
	slot_id, worker_id, result, review_feedback, created_by, created_at_ms, started_at_ms, completed_at_ms, updated_at_ms`

func scanTask(row interface{ Scan(...any) error }) (*leapmuxv1.WorkflowTask, error) {
	t := &leapmuxv1.WorkflowTask{}
	var status, approval int32
	var result []byte
	if err := row.Scan(&t.Id, &t.RunId, &t.StepKey, &t.RoleKey, &t.Title, &t.Instructions, &status, &t.StatusDetail, &approval,
		&t.Attempt, &t.MaxAttempts, &t.SlotId, &t.WorkerId, &result, &t.ReviewFeedback, &t.CreatedBy,
		&t.CreatedAtMs, &t.StartedAtMs, &t.CompletedAtMs, &t.UpdatedAtMs); err != nil {
		return nil, err
	}
	t.Status = leapmuxv1.WorkflowTaskStatus(status)
	t.Approval = leapmuxv1.WorkflowApprovalGate(approval)
	if len(result) > 0 {
		t.Result = &leapmuxv1.WorkflowTaskResult{}
		if err := proto.Unmarshal(result, t.Result); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func insertTask(ctx context.Context, q querier, t *leapmuxv1.WorkflowTask) error {
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM workflow_tasks WHERE run_id = ?`, t.GetRunId()).Scan(&seq); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO workflow_tasks (id, run_id, seq, step_key, role_key, title, instructions, status, status_detail,
		approval, attempt, max_attempts, slot_id, worker_id, result, review_feedback, created_by, created_at_ms, started_at_ms, completed_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?)`,
		t.GetId(), t.GetRunId(), seq, t.GetStepKey(), t.GetRoleKey(), t.GetTitle(), t.GetInstructions(), int32(t.GetStatus()), t.GetStatusDetail(),
		int32(t.GetApproval()), t.GetAttempt(), t.GetMaxAttempts(), t.GetSlotId(), t.GetWorkerId(), t.GetReviewFeedback(), t.GetCreatedBy(),
		t.GetCreatedAtMs(), t.GetStartedAtMs(), t.GetCompletedAtMs(), t.GetUpdatedAtMs()); err != nil {
		return err
	}
	for _, dep := range t.GetDependsOn() {
		if _, err := q.ExecContext(ctx, `INSERT INTO workflow_task_deps (task_id, depends_on) VALUES (?, ?)`, t.GetId(), dep); err != nil {
			return err
		}
	}
	return nil
}

func updateTask(ctx context.Context, q querier, t *leapmuxv1.WorkflowTask) error {
	var result []byte
	if t.GetResult() != nil {
		var err error
		if result, err = marshal(t.GetResult()); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, `UPDATE workflow_tasks SET instructions = ?, status = ?, status_detail = ?, attempt = ?, max_attempts = ?,
		slot_id = ?, worker_id = ?, result = ?, review_feedback = ?, started_at_ms = ?, completed_at_ms = ?, updated_at_ms = ? WHERE id = ?`,
		t.GetInstructions(), int32(t.GetStatus()), t.GetStatusDetail(), t.GetAttempt(), t.GetMaxAttempts(),
		t.GetSlotId(), t.GetWorkerId(), result, t.GetReviewFeedback(), t.GetStartedAtMs(), t.GetCompletedAtMs(), t.GetUpdatedAtMs(), t.GetId())
	return err
}

func listTasks(ctx context.Context, q querier, runID string) ([]*leapmuxv1.WorkflowTask, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+taskColumns+` FROM workflow_tasks WHERE run_id = ? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	var out []*leapmuxv1.WorkflowTask
	byID := map[string]*leapmuxv1.WorkflowTask{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, t)
		byID[t.GetId()] = t
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	deps, err := q.QueryContext(ctx, `SELECT d.task_id, d.depends_on FROM workflow_task_deps d
		JOIN workflow_tasks t ON t.id = d.task_id WHERE t.run_id = ? ORDER BY d.rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = deps.Close() }()
	for deps.Next() {
		var taskID, dependsOn string
		if err := deps.Scan(&taskID, &dependsOn); err != nil {
			return nil, err
		}
		if t := byID[taskID]; t != nil {
			t.DependsOn = append(t.DependsOn, dependsOn)
		}
	}
	return out, deps.Err()
}

// --- Approvals ---

const approvalColumns = `id, run_id, task_id, kind, status, title, details, requested_by, decision_note, created_at_ms, decided_at_ms`

func scanApproval(row interface{ Scan(...any) error }) (*leapmuxv1.WorkflowApproval, error) {
	a := &leapmuxv1.WorkflowApproval{}
	var kind, status int32
	if err := row.Scan(&a.Id, &a.RunId, &a.TaskId, &kind, &status, &a.Title, &a.Details, &a.RequestedBy, &a.DecisionNote,
		&a.CreatedAtMs, &a.DecidedAtMs); err != nil {
		return nil, err
	}
	a.Kind = leapmuxv1.WorkflowApprovalKind(kind)
	a.Status = leapmuxv1.WorkflowApprovalStatus(status)
	return a, nil
}

func insertApproval(ctx context.Context, q querier, a *leapmuxv1.WorkflowApproval) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workflow_approvals (`+approvalColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.GetId(), a.GetRunId(), a.GetTaskId(), int32(a.GetKind()), int32(a.GetStatus()), a.GetTitle(), a.GetDetails(),
		a.GetRequestedBy(), a.GetDecisionNote(), a.GetCreatedAtMs(), a.GetDecidedAtMs())
	return err
}

func updateApproval(ctx context.Context, q querier, a *leapmuxv1.WorkflowApproval) error {
	_, err := q.ExecContext(ctx, `UPDATE workflow_approvals SET status = ?, decision_note = ?, decided_at_ms = ? WHERE id = ?`,
		int32(a.GetStatus()), a.GetDecisionNote(), a.GetDecidedAtMs(), a.GetId())
	return err
}

func listApprovals(ctx context.Context, q querier, runID string) ([]*leapmuxv1.WorkflowApproval, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+approvalColumns+` FROM workflow_approvals WHERE run_id = ? ORDER BY created_at_ms, id`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowApproval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- Events ---

func insertEvent(ctx context.Context, q querier, e *leapmuxv1.WorkflowEvent) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workflow_events (run_id, task_id, kind, actor, message, at_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		e.GetRunId(), e.GetTaskId(), int32(e.GetKind()), e.GetActor(), e.GetMessage(), e.GetAtMs())
	return err
}

const recentEventLimit = 200

func listRecentEvents(ctx context.Context, q querier, runID string) ([]*leapmuxv1.WorkflowEvent, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, run_id, task_id, kind, actor, message, at_ms FROM (
		SELECT * FROM workflow_events WHERE run_id = ? ORDER BY id DESC LIMIT ?) ORDER BY id`, runID, recentEventLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.WorkflowEvent
	for rows.Next() {
		e := &leapmuxv1.WorkflowEvent{}
		var kind int32
		if err := rows.Scan(&e.Id, &e.RunId, &e.TaskId, &kind, &e.Actor, &e.Message, &e.AtMs); err != nil {
			return nil, err
		}
		e.Kind = leapmuxv1.WorkflowEventKind(kind)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Outbox ---

type outboxItem struct {
	id, runID, slotID, taskID, text string
	attempts                        int
}

func insertOutbox(ctx context.Context, q querier, item outboxItem, nowMs int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO workflow_outbox (id, run_id, slot_id, task_id, text, created_at_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		item.id, item.runID, item.slotID, item.taskID, item.text, nowMs)
	return err
}

func pendingOutbox(ctx context.Context, q querier, runID string) ([]outboxItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, run_id, slot_id, task_id, text, attempts FROM workflow_outbox
		WHERE run_id = ? AND delivered_at_ms = 0 ORDER BY created_at_ms, rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []outboxItem
	for rows.Next() {
		var it outboxItem
		if err := rows.Scan(&it.id, &it.runID, &it.slotID, &it.taskID, &it.text, &it.attempts); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func markOutbox(ctx context.Context, q querier, id string, deliveredAtMs int64, lastError string) error {
	_, err := q.ExecContext(ctx, `UPDATE workflow_outbox SET delivered_at_ms = ?, attempts = attempts + 1, last_error = ? WHERE id = ?`,
		deliveredAtMs, lastError, id)
	return err
}

// --- Hosting bindings ---

// PutBinding records that a local agent belongs to a run.
func (s *Store) PutBinding(ctx context.Context, agentID string, b *leapmuxv1.WorkflowBinding) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_agent_bindings (agent_id, run_id, slot_id, role_key, coordinator_worker_id, token, confine)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, agentID, b.GetRunId(), b.GetSlotId(), b.GetRoleKey(), b.GetCoordinatorWorkerId(), b.GetToken(), b.GetConfine())
	return err
}

// Binding returns a local agent's binding, or nil when it has none.
func (s *Store) Binding(ctx context.Context, agentID string) (*leapmuxv1.WorkflowBinding, error) {
	b := &leapmuxv1.WorkflowBinding{}
	err := s.db.QueryRowContext(ctx, `SELECT run_id, slot_id, role_key, coordinator_worker_id, token, confine FROM workflow_agent_bindings WHERE agent_id = ?`,
		agentID).Scan(&b.RunId, &b.SlotId, &b.RoleKey, &b.CoordinatorWorkerId, &b.Token, &b.Confine)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workflow binding: %w", err)
	}
	return b, nil
}

// BindingsForRun lists the local agents opened for a run.
func (s *Store) BindingsForRun(ctx context.Context, runID string) ([]*leapmuxv1.FindWorkflowAgentsResponse_Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT slot_id, agent_id FROM workflow_agent_bindings WHERE run_id = ? ORDER BY agent_id`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*leapmuxv1.FindWorkflowAgentsResponse_Entry
	for rows.Next() {
		e := &leapmuxv1.FindWorkflowAgentsResponse_Entry{}
		if err := rows.Scan(&e.SlotId, &e.AgentId); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
