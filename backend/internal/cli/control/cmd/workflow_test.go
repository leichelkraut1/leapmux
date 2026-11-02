package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
)

func captureWorkflowOut(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := control.Out
	control.Out = &buf
	t.Cleanup(func() { control.Out = prev })
	return &buf
}

func errorCode(t *testing.T, out *bytes.Buffer) string {
	t.Helper()
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &env), out.String())
	return env.Error["code"]
}

func TestRunWorkflowContext(t *testing.T) {
	t.Setenv("LEAPMUX_WORKFLOW_RUN_ID", "")
	t.Setenv("LEAPMUX_WORKFLOW_TOKEN", "")
	t.Setenv("LEAPMUX_WORKFLOW_COORDINATOR_WORKER_ID", "")
	out := captureWorkflowOut(t)
	err := RunWorkflowContext(nil, nil)
	require.Error(t, err)
	assert.Equal(t, "not_a_workflow_agent", errorCode(t, out), "an ordinary process is told it is not in a workflow")
}

func TestRunWorkflowReport(t *testing.T) {
	t.Setenv("LEAPMUX_WORKFLOW_RUN_ID", "run-1")
	t.Setenv("LEAPMUX_WORKFLOW_TOKEN", "token")
	t.Setenv("LEAPMUX_WORKFLOW_COORDINATOR_WORKER_ID", "lead")
	for name, args := range map[string][]string{
		"refuses a missing outcome":        {"--task", "t", "--summary", "s"},
		"refuses an unknown outcome":       {"--task", "t", "--outcome", "great", "--summary", "s"},
		"refuses both test verdicts":       {"--task", "t", "--outcome", "completed", "--summary", "s", "--tests-passed", "--tests-failed"},
		"refuses evidence without a label": {"--task", "t", "--outcome", "completed", "--summary", "s", "--evidence", "=x"},
		"refuses a report naming no task":  {"--outcome", "completed", "--summary", "s"},
	} {
		t.Run(name, func(t *testing.T) {
			out := captureWorkflowOut(t)
			require.Error(t, RunWorkflowReport(nil, args))
			assert.Equal(t, "invalid_request", errorCode(t, out))
		})
	}
}

func TestParseApprovalGate(t *testing.T) {
	for in, want := range map[string]leapmuxv1.WorkflowApprovalGate{
		"":                leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_NONE,
		"none":            leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_NONE,
		"before-start":    leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_START,
		"Before-Complete": leapmuxv1.WorkflowApprovalGate_WORKFLOW_APPROVAL_GATE_BEFORE_COMPLETE,
	} {
		got, err := parseApprovalGate(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	captureWorkflowOut(t)
	_, err := parseApprovalGate("sometimes")
	assert.Error(t, err)
}

func TestStringList(t *testing.T) {
	var l stringList
	require.NoError(t, l.Set("a, b"))
	require.NoError(t, l.Set("c"))
	assert.Equal(t, stringList{"a", "b", "c"}, l, "a comma and a repeat both separate values")
}
