package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// Idle release stops the process of an open agent that has had nothing to do
// for Config.AgentIdleRelease, and keeps everything else: the tab, its
// transcript and the session it resumes. The next message starts the process
// again through ensureAgentRunning, resuming the stored session, exactly as
// the first message after a worker restart does.
//
// Why: a provider process holds its conversation for as long as it runs.
// Claude Code records every live process in its session registry, and a
// conversation that a LeapMux tab keeps running reads as "still open somewhere
// else" in every other client -- for days, because nothing ever stopped an
// idle tab's process. It also keeps one CLI and its memory alive per tab.
//
// "Nothing to do" is the same rule every other surface reads
// (AgentActivitySnapshot): no turn, no prompt waiting for the user, no
// background task. The queue must be empty too, and no message may have been
// written for the whole window. The release re-checks all of it under the
// agent's lifecycle lock, which every start also takes, so a message that
// arrives during the check is never cut off.

// idleReleaseTick is how often the releaser looks. A release can therefore
// come up to one tick after the window ends, which is nothing next to an hour.
const idleReleaseTick = time.Minute

// idleReleaser remembers when it first saw each process, so a process that
// was just started on an old conversation is not released at once.
type idleReleaser struct {
	svc       *Service
	mu        sync.Mutex
	firstSeen map[string]time.Time
}

// RunIdleRelease runs idle release until ctx ends. It does nothing when the
// window is zero.
func (svc *Service) RunIdleRelease(ctx context.Context) {
	if svc.AgentIdleRelease <= 0 {
		return
	}
	r := &idleReleaser{svc: svc, firstSeen: map[string]time.Time{}}
	ticker := svc.Clock.NewTicker(idleReleaseTick, "agent", "idle-release")
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.pass(ctx)
		}
	}
}

// pass considers every running agent once.
func (r *idleReleaser) pass(ctx context.Context) {
	svc := r.svc
	if svc.shuttingDown.Load() {
		return
	}
	now := svc.Clock.Now()
	running := svc.Agents.ListAgentIDs()
	alive := make(map[string]bool, len(running))
	r.mu.Lock()
	for _, id := range running {
		alive[id] = true
		if _, ok := r.firstSeen[id]; !ok {
			r.firstSeen[id] = now
		}
	}
	for id := range r.firstSeen {
		if !alive[id] {
			delete(r.firstSeen, id)
		}
	}
	r.mu.Unlock()
	for _, id := range running {
		if ctx.Err() != nil || svc.shuttingDown.Load() {
			return
		}
		if r.idleSince(ctx, id).IsZero() {
			continue
		}
		r.release(ctx, id, now)
	}
}

// idleSince reports when the agent last did anything, or zero when it is not
// idle now or cannot be released at all.
func (r *idleReleaser) idleSince(ctx context.Context, agentID string) time.Time {
	svc := r.svc
	row, err := svc.getAgentByID(ctx, agentID)
	if err != nil || row.ParentAgentID.Valid || row.ClosedAt.Valid {
		return time.Time{}
	}
	act := svc.Output.AgentActivitySnapshot(agentID, agentID)
	if act.State != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE || act.ActiveTasks > 0 {
		return time.Time{}
	}
	// The published state too: while a settle window is open it still says
	// WORKING, and a turn that just ended is not idle yet.
	if svc.Output.AgentActivityPublished(agentID, agentID) != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE {
		return time.Time{}
	}
	snap, err := svc.InputQueue.Snapshot(ctx, agentID)
	if err != nil || snap.ActiveTurn || len(snap.Items) > 0 {
		return time.Time{}
	}
	since := row.CreatedAt.Time
	if last, err := svc.Queries.GetLatestMessageByAgentID(ctx, agentID); err == nil {
		since = latest(since, last.CreatedAt.Time)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}
	}
	r.mu.Lock()
	seen := r.firstSeen[agentID]
	r.mu.Unlock()
	return latest(since, seen)
}

// release stops the process when the window has passed, re-checking under the
// lifecycle lock that every start of this agent takes.
func (r *idleReleaser) release(ctx context.Context, agentID string, now time.Time) {
	svc := r.svc
	since := r.idleSince(ctx, agentID)
	if since.IsZero() || now.Sub(since) < svc.AgentIdleRelease {
		return
	}
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()
	since = r.idleSince(ctx, agentID)
	if since.IsZero() || now.Sub(since) < svc.AgentIdleRelease || !svc.Agents.HasAgent(agentID) {
		return
	}
	row, err := svc.getAgentByID(ctx, agentID)
	if err != nil {
		return
	}
	slog.Info("agent idle release: stopping an idle agent's process; its next message resumes the session",
		"agent_id", agentID, "idle_for", now.Sub(since).Round(time.Minute))
	// An explicit stop: the exit handler neither pauses the queue nor reports
	// a crash, and the goal reads DORMANT until the next start.
	if !svc.Agents.DiscardOutputAndStopAgent(agentID) {
		return
	}
	r.mu.Lock()
	delete(r.firstSeen, agentID)
	r.mu.Unlock()
	svc.broadcastAgentInactive(&row)
}

// releasedWhileIdle reports whether the boot sweep should leave a tab cold
// because it has been idle longer than the release window: restarting its
// process would only hold its conversation until the releaser stops it again.
func (svc *Service) releasedWhileIdle(ctx context.Context, row db.Agent) bool {
	if svc.AgentIdleRelease <= 0 {
		return false
	}
	since := row.CreatedAt.Time
	if last, err := svc.Queries.GetLatestMessageByAgentID(ctx, row.ID); err == nil {
		since = latest(since, last.CreatedAt.Time)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return svc.Clock.Now().Sub(since) >= svc.AgentIdleRelease
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
