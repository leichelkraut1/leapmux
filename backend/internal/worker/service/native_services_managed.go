package service

import (
	"context"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const maxNativeManagedIdentities = 4096

type nativeManagedSessionQueries interface {
	ListAgentTreeNativeSessionIdentities(context.Context, string) ([]db.ListAgentTreeNativeSessionIdentitiesRow, error)
}
type nativeManagedSessionKey struct {
	provider  leapmuxv1.AgentProvider
	sessionID string
}

// excludeManagedNativeServiceSessions only considers current Manager runtimes,
// plus their still-open virtual children. An inactive tab or closed history row
// cannot hide an external app session. Neither options nor messages are read.
func excludeManagedNativeServiceSessions(ctx context.Context, queries nativeManagedSessionQueries, liveIDs func() []string, result *leapmuxv1.InspectNativeAgentProcessesResponse) {
	if len(result.ServiceGroups) == 0 {
		return
	}
	owners := liveIDs()
	if len(owners) == 0 {
		return
	}
	if len(owners) > maxNativeManagedIdentities || queries == nil {
		invalidateNativeServiceSessions(result)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	type tree struct {
		owner string
		rows  []db.ListAgentTreeNativeSessionIdentitiesRow
	}
	trees := make([]tree, 0, len(owners))
	count := 0
	for _, owner := range owners {
		rows, err := queries.ListAgentTreeNativeSessionIdentities(ctx, owner)
		count += len(rows)
		if err != nil || ctx.Err() != nil || count > maxNativeManagedIdentities {
			invalidateNativeServiceSessions(result)
			return
		}
		trees = append(trees, tree{owner: owner, rows: rows})
	}
	// A process that ended while SQLite was queried no longer owns an external
	// observation. Newly created runtimes are handled by the next bounded poll.
	current := map[string]bool{}
	for _, id := range liveIDs() {
		current[id] = true
	}
	managed := map[nativeManagedSessionKey]bool{}
	for _, tree := range trees {
		if !current[tree.owner] {
			continue
		}
		for _, row := range tree.rows {
			for _, id := range []string{row.AgentSessionID, row.PendingResumeSessionID} {
				if id != "" {
					managed[nativeManagedSessionKey{provider: row.AgentProvider, sessionID: id}] = true
				}
			}
		}
	}
	for _, group := range result.ServiceGroups {
		group.Sessions = slices.DeleteFunc(group.Sessions, func(session *leapmuxv1.NativeAgentSession) bool {
			return managed[nativeManagedSessionKey{provider: group.AgentProvider, sessionID: session.SessionId}]
		})
	}
}
func invalidateNativeServiceSessions(result *leapmuxv1.InspectNativeAgentProcessesResponse) {
	for _, group := range result.ServiceGroups {
		group.Available = false
		group.Partial = true
		group.Sessions = nil
	}
}
