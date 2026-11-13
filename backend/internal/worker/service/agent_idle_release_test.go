package service

import (
	"context"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// idleReleaseFixture is a service with one open, running root agent and the
// release window set to an hour, on a mock clock at the real present (the
// rows' created_at is written by SQLite from the real clock).
func idleReleaseFixture(t *testing.T) (*Service, *quartz.Mock, *idleReleaser, agent.ProviderServices) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(time.Now())
	svc, _, _ := setupTestService(t, withClock(clock))
	svc.AgentIdleRelease = time.Hour
	seedOpenAgent(t, svc, "agent-1", true)
	svc.Agents.PutAgentForTest("agent-1", &agenttest.IdleAgent{})
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	svc.Output.NoteAgentProcessStarted("agent-1")
	return svc, clock, &idleReleaser{svc: svc, firstSeen: map[string]time.Time{}}, sink
}

func TestIdleRelease_StopsTheProcessOfAnAgentIdleForTheWholeWindow(t *testing.T) {
	t.Parallel()
	svc, clock, r, _ := idleReleaseFixture(t)

	r.pass(t.Context())
	assert.True(t, svc.Agents.HasAgent("agent-1"), "a process just seen is not released")

	clock.Advance(59 * time.Minute).MustWait(t.Context())
	r.pass(t.Context())
	assert.True(t, svc.Agents.HasAgent("agent-1"), "not before the window ends")

	clock.Advance(2 * time.Minute).MustWait(t.Context())
	r.pass(t.Context())
	assert.False(t, svc.Agents.HasAgent("agent-1"), "released after an hour with nothing to do")

	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "the tab stays open")
	assert.Equal(t, "session-agent-1", row.AgentSessionID, "and keeps the session its next message resumes")
}

func TestIdleRelease_NeverStopsAnAgentInATurn(t *testing.T) {
	t.Parallel()
	svc, clock, r, sink := idleReleaseFixture(t)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)

	r.pass(t.Context())
	clock.Advance(3 * time.Hour).MustWait(t.Context())
	r.pass(t.Context())
	assert.True(t, svc.Agents.HasAgent("agent-1"), "a turn that runs for hours is work, not idleness")
}

func TestIdleRelease_NeverStopsAnAgentWithQueuedInput(t *testing.T) {
	t.Parallel()
	svc, clock, r, _ := idleReleaseFixture(t)
	_, err := svc.InputQueue.Pause(t.Context(), "agent-1", leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)
	_, err = svc.InputQueue.Enqueue(t.Context(), inputqueue.NewItem{
		ID: "queued-1", AgentID: "agent-1", Text: "later", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)

	r.pass(t.Context())
	clock.Advance(3 * time.Hour).MustWait(t.Context())
	r.pass(t.Context())
	assert.True(t, svc.Agents.HasAgent("agent-1"), "a message waiting to be sent keeps its agent")
}

func TestIdleRelease_IsOffWhenTheWindowIsZero(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := idleReleaseFixture(t)
	svc.AgentIdleRelease = 0
	done := make(chan struct{})
	go func() {
		svc.RunIdleRelease(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunIdleRelease ran with the release disabled")
	}
	row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.False(t, svc.releasedWhileIdle(t.Context(), row))
}

// setAgentCreatedAt moves an agent's creation time, which stands in for its
// last activity when it has no messages.
func setAgentCreatedAt(t *testing.T, svc *Service, agentID string, at time.Time) {
	t.Helper()
	_, err := svc.DB.ExecContext(t.Context(), `UPDATE agents SET created_at = ? WHERE id = ?`, at.UTC().Format("2006-01-02T15:04:05.000Z"), agentID)
	require.NoError(t, err)
}

func TestAgentResume_LeavesATabIdleLongerThanTheReleaseWindowCold(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	svc.AgentIdleRelease = time.Hour
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-quiet", true)
	seedOpenAgent(t, svc, "agent-recent", true)
	setAgentCreatedAt(t, svc, "agent-quiet", time.Now().Add(-3*time.Hour))

	runSweep(t, svc)
	assert.Equal(t, []string{"agent-recent"}, rec.ids(), "a tab quiet for hours stays cold until its next message")
}
