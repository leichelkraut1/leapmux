package workflow

import (
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

// notePrefix marks every message the engine writes, so a participant can tell
// a workflow instruction from the user typing in its chat.
const notePrefix = "[LeapMux workflow]"

// workerNamer resolves a computer id to the name the user gave it.
type workerNamer func(workerID string) string

func roleSummary(role *leapmuxv1.WorkflowRole) string {
	agent := role.GetAgent()
	parts := []string{agentlabels.CLIAlias(agent.GetAgentProvider())}
	if model := agent.GetOptions()["model"]; model != "" {
		parts = append(parts, model)
	}
	if effort := agent.GetOptions()["effort"]; effort != "" {
		parts = append(parts, effort+" effort")
	}
	return strings.Join(parts, ", ")
}

func placementSummary(a *leapmuxv1.WorkflowRoleAssignment, name workerNamer) string {
	p := a.GetRole().GetAgent().GetPlacement()
	switch p.GetStrategy() {
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_SPECIFIC_WORKER:
		return "pinned to " + name(p.GetWorkerId())
	case leapmuxv1.WorkerPlacementStrategy_WORKER_PLACEMENT_STRATEGY_CAPABILITY:
		where := "any computer with " + strings.Join(p.GetRequiredCapabilities(), ", ")
		if a.GetWorkerId() != "" {
			where += " (currently " + name(a.GetWorkerId()) + ")"
		}
		return where
	default:
		if a.GetWorkerId() != "" {
			return "any computer (currently " + name(a.GetWorkerId()) + ")"
		}
		return "any computer"
	}
}

// leadBriefing is the Lead's first message: the objective, the team, the
// default process, the rules, and the commands that drive the run.
func leadBriefing(run *leapmuxv1.WorkflowRun, roles []*leapmuxv1.WorkflowRoleAssignment, tasks []*leapmuxv1.WorkflowTask, name workerNamer) string {
	t := run.GetTemplate()
	var b strings.Builder
	fmt.Fprintf(&b, "%s You are the **Lead** of the workflow run %q (template %q).\n\n", notePrefix, run.GetName(), t.GetName())
	b.WriteString("## Objective\n\n")
	b.WriteString(strings.TrimSpace(run.GetObjective()))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Project directory: `%s`", run.GetWorkingDir())
	if run.GetBaseBranch() != "" {
		fmt.Fprintf(&b, ", base branch `%s`", run.GetBaseBranch())
	}
	b.WriteString(".\n\n")
	if instructions := strings.TrimSpace(leadRole(rolesOf(roles)).GetAgent().GetInstructions()); instructions != "" {
		b.WriteString("## Your role\n\n")
		b.WriteString(instructions)
		b.WriteString("\n\n")
	}
	if instructions := strings.TrimSpace(t.GetInstructions()); instructions != "" {
		b.WriteString("## Process\n\n")
		b.WriteString(instructions)
		b.WriteString("\n\n")
	}
	if criteria := strings.TrimSpace(t.GetCompletionCriteria()); criteria != "" {
		b.WriteString("## Completion criteria\n\n")
		b.WriteString(criteria)
		b.WriteString("\n\n")
	}
	b.WriteString("## Team\n\n")
	for _, a := range roles {
		r := a.GetRole()
		if r.GetIsLead() {
			continue
		}
		fmt.Fprintf(&b, "- `%s` - %s (%s; %s)", r.GetKey(), r.GetName(), roleSummary(r), placementSummary(a, name))
		if outs := r.GetRequiredOutputs(); len(outs) > 0 {
			fmt.Fprintf(&b, "; reports must include: %s", strings.Join(outs, ", "))
		}
		if r.GetReviewRequired() {
			b.WriteString("; you review its results")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nAgents are started on their computers when you delegate to them, not before.\n\n")
	if len(tasks) > 0 {
		b.WriteString("## Planned tasks\n\nThese come from the template. Release one with `delegate --task <id>`, adding objective-specific instructions.\n\n")
		for _, task := range tasks {
			fmt.Fprintf(&b, "- `%s` %s -> `%s`", task.GetId(), task.GetTitle(), task.GetRoleKey())
			if len(task.GetDependsOn()) > 0 {
				fmt.Fprintf(&b, " (after %s)", strings.Join(task.GetDependsOn(), ", "))
			}
			if task.GetApproval() == leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_START {
				b.WriteString(" [needs human approval to start]")
			}
			if task.GetStatus() == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_QUEUED {
				b.WriteString(" [already released]")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Rules\n\n")
	b.WriteString("- Do the coordination yourself; delegate implementation, review and testing to the team.\n")
	if t.GetConfineAgents() {
		b.WriteString("- Your control socket reaches only this workflow.\n")
	} else {
		b.WriteString("- Every other `leapmux control` command works too: you and the team may open helper agents (`leapmux control tab open --type agent ...`) whenever that gets the work done faster. The run tracks only the tasks delegated here.\n")
	}
	b.WriteString("- A task is done only when the engine accepts its structured result, never because an agent says so in chat.\n")
	b.WriteString("- Every task across computers must name an exact git revision (branch and commit SHA).\n")
	if ops := t.GetSensitiveOperations(); len(ops) > 0 {
		fmt.Fprintf(&b, "- Request human approval before: %s. Wait for the decision before proceeding.\n", strings.Join(ops, "; "))
	}
	if t.GetRequireFinalApproval() {
		b.WriteString("- Completing the run asks the user for final approval.\n")
	}
	b.WriteString("\n## Commands\n\nRun these in your shell. Each prints JSON. You receive updates as messages like this one.\n\n")
	b.WriteString("```\n")
	b.WriteString("leapmux control workflow context                     # team, tasks, approvals\n")
	b.WriteString("leapmux control workflow delegate --role <key> --title <t> --instructions <text|-> [--depends-on <task-id>] [--approval before-start|before-complete]\n")
	b.WriteString("leapmux control workflow delegate --task <task-id> [--instructions <extra>]   # release a planned task\n")
	b.WriteString("leapmux control workflow send --task <task-id> --message <text>              # follow-up to its agent\n")
	b.WriteString("leapmux control workflow review --task <task-id> --accept | --reject --feedback <text>\n")
	b.WriteString("leapmux control workflow request-approval --title <t> --details <text>\n")
	b.WriteString("leapmux control workflow complete --summary <text> [--failed]\n")
	b.WriteString("```\n\n")
	b.WriteString("Start by planning, then delegate the first task.\n")
	return b.String()
}

func rolesOf(assignments []*leapmuxv1.WorkflowRoleAssignment) []*leapmuxv1.WorkflowRole {
	out := make([]*leapmuxv1.WorkflowRole, 0, len(assignments))
	for _, a := range assignments {
		out = append(out, a.GetRole())
	}
	return out
}

// taskDispatch is the message that hands a task to its agent. The first
// message a slot receives also carries the role briefing.
func taskDispatch(run *leapmuxv1.WorkflowRun, role *leapmuxv1.WorkflowRole, task *leapmuxv1.WorkflowTask, briefing bool) string {
	var b strings.Builder
	if briefing {
		fmt.Fprintf(&b, "%s You are the **%s** in the workflow run %q, coordinated by a Lead agent.\n\n", notePrefix, role.GetName(), run.GetName())
		if instructions := strings.TrimSpace(role.GetAgent().GetInstructions()); instructions != "" {
			b.WriteString("## Your role\n\n")
			b.WriteString(instructions)
			b.WriteString("\n\n")
		}
		b.WriteString("## Objective of the run\n\n")
		b.WriteString(strings.TrimSpace(run.GetObjective()))
		b.WriteString("\n\n")
		if ops := run.GetTemplate().GetSensitiveOperations(); len(ops) > 0 {
			fmt.Fprintf(&b, "Never do any of these without an approved request from the Lead: %s.\n\n", strings.Join(ops, "; "))
		}
		if !run.GetTemplate().GetConfineAgents() {
			b.WriteString("You may open helper agents with `leapmux control tab open --type agent ...` when that helps; report the task's result yourself.\n\n")
		}
	} else {
		fmt.Fprintf(&b, "%s ", notePrefix)
	}
	if task.GetAttempt() > 1 {
		fmt.Fprintf(&b, "## Task `%s`: %s (attempt %d of %d)\n\n", task.GetId(), task.GetTitle(), task.GetAttempt(), task.GetMaxAttempts())
	} else {
		fmt.Fprintf(&b, "## Task `%s`: %s\n\n", task.GetId(), task.GetTitle())
	}
	b.WriteString(strings.TrimSpace(task.GetInstructions()))
	b.WriteString("\n\n")
	if feedback := strings.TrimSpace(task.GetReviewFeedback()); feedback != "" && task.GetAttempt() > 1 {
		b.WriteString("### Feedback on the previous attempt\n\n")
		b.WriteString(feedback)
		b.WriteString("\n\n")
	}
	b.WriteString("When you finish, report a structured result. Do not just reply in chat.\n\n```\n")
	fmt.Fprintf(&b, "leapmux control workflow report --task %s --outcome completed|failed|blocked --summary <text>", task.GetId())
	outs := role.GetRequiredOutputs()
	for _, o := range outs {
		switch o {
		case OutputCommit:
			b.WriteString(" --branch <name> --commit <sha>")
		case OutputTests:
			b.WriteString(" --tests-passed|--tests-failed --test-summary <text>")
		case OutputArtifacts:
			b.WriteString(" --artifact <path-or-url>")
		}
	}
	b.WriteString(" [--evidence label=value]\n")
	fmt.Fprintf(&b, "leapmux control workflow progress --task %s --note <text>   # optional progress note\n", task.GetId())
	b.WriteString("```\n")
	if len(outs) > 0 {
		fmt.Fprintf(&b, "\nA completed report must include: %s. The engine refuses one that does not.\n", strings.Join(outs, ", "))
	}
	return b.String()
}

func leadNote(format string, args ...any) string {
	return notePrefix + " " + fmt.Sprintf(format, args...)
}

func resultSummary(r *leapmuxv1.WorkflowTaskResult) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Outcome: %s\n\n%s\n", outcomeWord(r.GetOutcome()), strings.TrimSpace(r.GetSummary()))
	if r.GetGitCommit() != "" {
		fmt.Fprintf(&b, "\nRevision: %s %s\n", r.GetGitBranch(), r.GetGitCommit())
	}
	if r.GetTestsRan() {
		verdict := "FAILED"
		if r.GetTestsPassed() {
			verdict = "passed"
		}
		fmt.Fprintf(&b, "\nTests %s: %s\n", verdict, strings.TrimSpace(r.GetTestSummary()))
	}
	for _, a := range r.GetArtifacts() {
		fmt.Fprintf(&b, "- artifact: %s\n", a)
	}
	for _, e := range r.GetEvidence() {
		fmt.Fprintf(&b, "- %s: %s\n", e.GetLabel(), e.GetValue())
	}
	return b.String()
}

func outcomeWord(o leapmuxv1.WorkflowTaskOutcome) string {
	switch o {
	case leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED:
		return "completed"
	case leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_FAILED:
		return "failed"
	case leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_BLOCKED:
		return "blocked"
	default:
		return "unknown"
	}
}
