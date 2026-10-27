package service

import (
	"context"
	"errors"
	"strconv"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeNativeProcessSource struct {
	*procutil.ProcessTable
	names     map[int]string
	users     map[int]string
	paths     map[int]string
	pathReads []int
}

func (s *fakeNativeProcessSource) ShortName(_ context.Context, pid int) (string, error) {
	return s.names[pid], nil
}
func (s *fakeNativeProcessSource) UserKey(_ context.Context, pid int) (string, error) {
	user, ok := s.users[pid]
	if !ok {
		return "", errors.New("owner inaccessible")
	}
	return user, nil
}
func (s *fakeNativeProcessSource) Executable(_ context.Context, pid int) (string, error) {
	s.pathReads = append(s.pathReads, pid)
	path, ok := s.paths[pid]
	if !ok {
		return "", errors.New("path inaccessible")
	}
	return path, nil
}

func nativeProcessFixture() *fakeNativeProcessSource {
	return &fakeNativeProcessSource{
		ProcessTable: procutil.NewProcessTable([]procutil.ProcessRecord{
			{PID: 100, PPID: 1}, {PID: 101, PPID: 100}, {PID: 102, PPID: 101},
			{PID: 30, PPID: 1}, {PID: 20, PPID: 1}, {PID: 11, PPID: 1}, {PID: 12, PPID: 1},
		}),
		names: map[int]string{100: "leapmux", 101: "pwsh", 102: "codex", 11: "codex", 12: "node", 20: "claude", 30: "Codex"},
		users: map[int]string{100: "owner", 11: "owner", 20: "someone-else", 30: "owner"},
		paths: map[int]string{11: "/private/owner/bin/codex", 30: "/Applications/Codex.app/Contents/MacOS/Codex"},
	}
}

func TestInspectNativeAgentProcessesScopesOwnerAndExcludesWorkerTree(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	source := nativeProcessFixture()
	result, err := inspectNativeAgentProcesses(context.Background(), source, 100, svc.Agents.Registry(), maxNativeProcesses)
	require.NoError(t, err)
	require.Len(t, result.Processes, 2)
	assert.Equal(t, int32(2), result.TotalCount)
	assert.Equal(t, []int{11, 30}, source.pathReads, "never read paths for managed, other-user, or unrecognized processes")
	assert.Equal(t, int32(11), result.Processes[0].Pid)
	assert.Equal(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_CLI, result.Processes[0].Kind)
	assert.Equal(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_DESKTOP, result.Processes[1].Kind)
	payload, err := protojson.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "/private")
	assert.NotContains(t, string(payload), "/Applications")
	assert.NotContains(t, string(payload), "someone-else")
}

func TestInspectNativeAgentProcessesCapsRowsWithoutLosingCount(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	result, err := inspectNativeAgentProcesses(context.Background(), nativeProcessFixture(), 100, svc.Agents.Registry(), 1)
	require.NoError(t, err)
	require.Len(t, result.Processes, 1)
	assert.Equal(t, int32(11), result.Processes[0].Pid, "PID ordering remains stable across OS snapshot ordering")
	assert.Equal(t, int32(2), result.TotalCount)
}

func TestInspectNativeAgentProcessesRefusesUnknownOwnership(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	for _, pid := range []int{100, 11} {
		t.Run(strconv.Itoa(pid), func(t *testing.T) {
			source := nativeProcessFixture()
			delete(source.users, pid)
			result, err := inspectNativeAgentProcesses(context.Background(), source, 100, svc.Agents.Registry(), maxNativeProcesses)
			require.Error(t, err)
			assert.Nil(t, result, "an unreadable scan must not masquerade as an empty successful scan")
			assert.NotContains(t, source.pathReads, pid)
		})
	}
}

func TestInspectNativeAgentProcessesRetainsUnknownKindWhenPathIsUnavailable(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	source := nativeProcessFixture()
	delete(source.paths, 11)
	result, err := inspectNativeAgentProcesses(context.Background(), source, 100, svc.Agents.Registry(), maxNativeProcesses)
	require.NoError(t, err)
	require.Len(t, result.Processes, 2)
	assert.Equal(t, leapmuxv1.NativeAgentProcessKind_NATIVE_AGENT_PROCESS_KIND_UNKNOWN, result.Processes[0].Kind)
}

func TestInspectNativeAgentProcessesHonorsCancellation(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := nativeProcessFixture()
	result, err := inspectNativeAgentProcesses(ctx, source, 100, svc.Agents.Registry(), maxNativeProcesses)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
	assert.Empty(t, source.pathReads)
}

type unreadableNativeProcessName struct{ nativeProcessSource }

func (s unreadableNativeProcessName) ShortName(context.Context, int) (string, error) {
	return "", errors.New("process metadata inaccessible")
}

func TestInspectNativeAgentProcessesReportsNameReadFailure(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	source := nativeProcessFixture()
	result, err := inspectNativeAgentProcesses(context.Background(), unreadableNativeProcessName{source}, 100, svc.Agents.Registry(), maxNativeProcesses)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Empty(t, source.pathReads)
}

type exitedNativeProcessSource struct {
	nativeProcessSource
	pid int
}

func (s exitedNativeProcessSource) UserKey(ctx context.Context, pid int) (string, error) {
	if pid == s.pid {
		return "", procutil.ErrObservedProcessGone
	}
	return s.nativeProcessSource.UserKey(ctx, pid)
}

func TestInspectNativeAgentProcessesSkipsOnlyVerifiedExitedCandidates(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	source := nativeProcessFixture()
	result, err := inspectNativeAgentProcesses(context.Background(), exitedNativeProcessSource{source, 11}, 100, svc.Agents.Registry(), maxNativeProcesses)
	require.NoError(t, err)
	require.Len(t, result.Processes, 1)
	assert.Equal(t, int32(30), result.Processes[0].Pid)
	assert.Equal(t, int32(1), result.TotalCount)
	assert.Equal(t, []int{30}, source.pathReads)

	source = nativeProcessFixture()
	result, err = inspectNativeAgentProcesses(context.Background(), exitedNativeProcessSource{source, 100}, 100, svc.Agents.Registry(), maxNativeProcesses)
	require.Error(t, err, "the scanner must establish its own owner even if that process disappeared")
	assert.Nil(t, result)
	assert.Empty(t, source.pathReads)
}
