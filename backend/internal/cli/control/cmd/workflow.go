package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// The `workflow` verbs are how a workflow agent talks to its run. They read
// the run, the coordinator and the credential from the LEAPMUX_WORKFLOW_*
// variables the worker sets on a workflow agent, and call the coordinator
// through the agent's own control socket -- the one door that socket leaves
// open.
const workflowCallTimeout = 60 * time.Second

type workflowEnv struct {
	coordinator string
	auth        *leapmuxv1.WorkflowAgentAuth
}

func loadWorkflowEnv() (workflowEnv, error) {
	env := workflowEnv{
		coordinator: os.Getenv("LEAPMUX_WORKFLOW_COORDINATOR_WORKER_ID"),
		auth: &leapmuxv1.WorkflowAgentAuth{
			RunId: os.Getenv("LEAPMUX_WORKFLOW_RUN_ID"),
			Token: os.Getenv("LEAPMUX_WORKFLOW_TOKEN"),
		},
	}
	if env.coordinator == "" || env.auth.GetRunId() == "" || env.auth.GetToken() == "" {
		return env, control.EmitError("not_a_workflow_agent",
			"this process is not a workflow agent: LEAPMUX_WORKFLOW_RUN_ID, LEAPMUX_WORKFLOW_TOKEN and LEAPMUX_WORKFLOW_COORDINATOR_WORKER_ID are unset")
	}
	return env, nil
}

// stringList is a repeatable flag; a comma also separates values.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}

// rawList is a repeatable flag kept whole, for values that may hold commas.
type rawList []string

func (l *rawList) String() string { return strings.Join(*l, "\n") }
func (l *rawList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// readValue resolves "-" to stdin, so long text need not be shell-quoted.
func readValue(v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	buf, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", control.EmitErrorWith("stdin_read_failed", err)
	}
	return string(buf), nil
}

// runWorkflow parses the verb's flags, then calls method on the coordinator
// with the request build returns, and prints the reply.
func runWorkflow(rawCtx any, args []string, setup func(fs *flag.FlagSet), build func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error)) error {
	cmd := asCtx(rawCtx)
	var hub string
	fs := flagSet(cmd, &hub)
	if setup != nil {
		setup(fs)
	}
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	env, err := loadWorkflowEnv()
	if err != nil {
		return err
	}
	method, in, out, err := build(env.auth)
	if err != nil {
		return err
	}
	c, err := control.NewClientFromEnv(hub)
	if err != nil {
		return control.EmitErrorWith("client_init_failed", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCallTimeout)
	defer cancel()
	if err := callInnerRPC(ctx, c, env.coordinator, method, in, out); err != nil {
		return err
	}
	data, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}.Marshal(out)
	if err != nil {
		return control.EmitErrorWith("encode_failed", err)
	}
	return control.EmitData(json.RawMessage(data))
}

func invalidRequest(format string, args ...any) error {
	return control.EmitError("invalid_request", fmt.Sprintf(format, args...))
}

func parseApprovalGate(v string) (leapmuxv1.WorkflowApprovalGate, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "none":
		return leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_NONE, nil
	case "before-start":
		return leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_START, nil
	case "before-complete":
		return leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_COMPLETE, nil
	}
	return 0, invalidRequest("--approval must be none, before-start or before-complete")
}

func parseOutcome(v string) (leapmuxv1.WorkflowTaskOutcome, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "completed", "complete", "done":
		return leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED, nil
	case "failed", "fail":
		return leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_FAILED, nil
	case "blocked":
		return leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_BLOCKED, nil
	}
	return 0, invalidRequest("--outcome must be completed, failed or blocked")
}

// RunWorkflowContext prints the run as this agent may see it.
func RunWorkflowContext(rawCtx any, args []string) error {
	return runWorkflow(rawCtx, args, nil, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		return "WorkflowAgentContext", &leapmuxv1.WorkflowAgentContextRequest{Auth: auth}, &leapmuxv1.WorkflowAgentContextResponse{}, nil
	})
}

// RunWorkflowDelegate creates a task for a role, or releases a planned one.
func RunWorkflowDelegate(rawCtx any, args []string) error {
	var role, title, instructions, approval, taskID string
	var maxAttempts uint
	var deps stringList
	return runWorkflow(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&role, "role", "", "role key to delegate to")
		fs.StringVar(&title, "title", "", "task title")
		fs.StringVar(&instructions, "instructions", "", "task instructions (\"-\" reads stdin)")
		fs.Var(&deps, "depends-on", "task id this task waits for (repeatable)")
		fs.StringVar(&approval, "approval", "none", "human approval gate: none, before-start, before-complete")
		fs.UintVar(&maxAttempts, "max-attempts", 0, "attempt limit (0 uses the template's default)")
		fs.StringVar(&taskID, "task", "", "release this planned task instead of creating one")
	}, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		text, err := readValue(instructions)
		if err != nil {
			return "", nil, nil, err
		}
		gate, err := parseApprovalGate(approval)
		if err != nil {
			return "", nil, nil, err
		}
		if taskID == "" && (role == "" || title == "" || strings.TrimSpace(text) == "") {
			return "", nil, nil, invalidRequest("--role, --title and --instructions are required (or --task to release a planned task)")
		}
		return "WorkflowAgentDelegate", &leapmuxv1.WorkflowAgentDelegateRequest{
			Auth: auth, RoleKey: role, Title: title, Instructions: text, DependsOn: deps,
			Approval: gate, MaxAttempts: uint32(maxAttempts), TaskId: taskID,
		}, &leapmuxv1.WorkflowAgentDelegateResponse{}, nil
	})
}

// RunWorkflowSend sends the Lead's follow-up to a task's agent.
func RunWorkflowSend(rawCtx any, args []string) error {
	var taskID, message string
	return runWorkflow(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&taskID, "task", "", "task whose agent receives the message")
		fs.StringVar(&message, "message", "", "message (\"-\" reads stdin)")
	}, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		text, err := readValue(message)
		if err != nil {
			return "", nil, nil, err
		}
		if taskID == "" || strings.TrimSpace(text) == "" {
			return "", nil, nil, invalidRequest("--task and --message are required")
		}
		return "WorkflowAgentSend", &leapmuxv1.WorkflowAgentSendRequest{Auth: auth, TaskId: taskID, Text: text}, &leapmuxv1.WorkflowAgentSendResponse{}, nil
	})
}

// RunWorkflowReport files a task's structured result.
func RunWorkflowReport(rawCtx any, args []string) error {
	var taskID, outcome, summary, branch, commit, testSummary string
	var testsPassed, testsFailed bool
	var artifacts, evidence rawList
	return runWorkflow(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&taskID, "task", "", "task id")
		fs.StringVar(&outcome, "outcome", "", "completed, failed or blocked")
		fs.StringVar(&summary, "summary", "", "what was done and found (\"-\" reads stdin)")
		fs.StringVar(&branch, "branch", "", "git branch of the result")
		fs.StringVar(&commit, "commit", "", "exact commit SHA of the result")
		fs.BoolVar(&testsPassed, "tests-passed", false, "the tests ran and passed")
		fs.BoolVar(&testsFailed, "tests-failed", false, "the tests ran and failed")
		fs.StringVar(&testSummary, "test-summary", "", "test command and its outcome")
		fs.Var(&artifacts, "artifact", "path or URL of an artifact (repeatable)")
		fs.Var(&evidence, "evidence", "label=value evidence (repeatable)")
	}, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		o, err := parseOutcome(outcome)
		if err != nil {
			return "", nil, nil, err
		}
		text, err := readValue(summary)
		if err != nil {
			return "", nil, nil, err
		}
		if taskID == "" {
			return "", nil, nil, invalidRequest("--task is required")
		}
		if testsPassed && testsFailed {
			return "", nil, nil, invalidRequest("--tests-passed and --tests-failed exclude each other")
		}
		result := &leapmuxv1.WorkflowTaskResult{
			Outcome: o, Summary: text, Artifacts: artifacts, GitBranch: branch, GitCommit: commit,
			TestSummary: testSummary, TestsRan: testsPassed || testsFailed, TestsPassed: testsPassed,
		}
		for _, e := range evidence {
			label, value, ok := strings.Cut(e, "=")
			if !ok || strings.TrimSpace(label) == "" {
				return "", nil, nil, invalidRequest("--evidence takes label=value, got %q", e)
			}
			result.Evidence = append(result.Evidence, &leapmuxv1.WorkflowEvidence{Label: strings.TrimSpace(label), Value: value})
		}
		return "WorkflowAgentReport", &leapmuxv1.WorkflowAgentReportRequest{Auth: auth, TaskId: taskID, Result: result}, &leapmuxv1.WorkflowAgentReportResponse{}, nil
	})
}

// RunWorkflowProgress records a progress note on a task.
func RunWorkflowProgress(rawCtx any, args []string) error {
	var taskID, note string
	return runWorkflow(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&taskID, "task", "", "task id")
		fs.StringVar(&note, "note", "", "progress note")
	}, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		if taskID == "" || strings.TrimSpace(note) == "" {
			return "", nil, nil, invalidRequest("--task and --note are required")
		}
		return "WorkflowAgentProgress", &leapmuxv1.WorkflowAgentProgressRequest{Auth: auth, TaskId: taskID, Note: note}, &leapmuxv1.WorkflowAgentProgressResponse{}, nil
	})
}

// RunWorkflowReview accepts a submitted result or sends it back.
func RunWorkflowReview(rawCtx any, args []string) error {
	var taskID, feedback string
	var accept, reject bool
	return runWorkflow(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&taskID, "task", "", "task id")
		fs.BoolVar(&accept, "accept", false, "accept the result")
		fs.BoolVar(&reject, "reject", false, "send the result back for another attempt")
		fs.StringVar(&feedback, "feedback", "", "what to correct (required with --reject; \"-\" reads stdin)")
	}, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		if taskID == "" || accept == reject {
			return "", nil, nil, invalidRequest("--task and exactly one of --accept or --reject are required")
		}
		text, err := readValue(feedback)
		if err != nil {
			return "", nil, nil, err
		}
		return "WorkflowAgentReview", &leapmuxv1.WorkflowAgentReviewRequest{Auth: auth, TaskId: taskID, Accept: accept, Feedback: text}, &leapmuxv1.WorkflowAgentReviewResponse{}, nil
	})
}

// RunWorkflowRequestApproval asks the user to approve a sensitive operation.
func RunWorkflowRequestApproval(rawCtx any, args []string) error {
	var title, details, taskID string
	return runWorkflow(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&title, "title", "", "what needs approval")
		fs.StringVar(&details, "details", "", "why, and what will happen (\"-\" reads stdin)")
		fs.StringVar(&taskID, "task", "", "task the request concerns (optional)")
	}, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		text, err := readValue(details)
		if err != nil {
			return "", nil, nil, err
		}
		if title == "" {
			return "", nil, nil, invalidRequest("--title is required")
		}
		return "WorkflowAgentRequestApproval", &leapmuxv1.WorkflowAgentRequestApprovalRequest{Auth: auth, Title: title, Details: text, TaskId: taskID},
			&leapmuxv1.WorkflowAgentRequestApprovalResponse{}, nil
	})
}

// RunWorkflowComplete finishes the run.
func RunWorkflowComplete(rawCtx any, args []string) error {
	var summary string
	var failed bool
	return runWorkflow(rawCtx, args, func(fs *flag.FlagSet) {
		fs.StringVar(&summary, "summary", "", "final outcome (\"-\" reads stdin)")
		fs.BoolVar(&failed, "failed", false, "the objective could not be met")
	}, func(auth *leapmuxv1.WorkflowAgentAuth) (string, proto.Message, proto.Message, error) {
		text, err := readValue(summary)
		if err != nil {
			return "", nil, nil, err
		}
		if strings.TrimSpace(text) == "" {
			return "", nil, nil, invalidRequest("--summary is required")
		}
		return "WorkflowAgentComplete", &leapmuxv1.WorkflowAgentCompleteRequest{Auth: auth, Summary: text, Failed: failed}, &leapmuxv1.WorkflowAgentCompleteResponse{}, nil
	})
}
