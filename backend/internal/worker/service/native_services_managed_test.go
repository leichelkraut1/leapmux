package service

import (
	"context"
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeNativeManagedQueries struct {
	rows  map[string][]db.ListAgentTreeNativeSessionIdentitiesRow
	err   error
	calls []string
}

func (q *fakeNativeManagedQueries) ListAgentTreeNativeSessionIdentities(_ context.Context, id string) ([]db.ListAgentTreeNativeSessionIdentitiesRow, error) {
	q.calls = append(q.calls, id)
	return q.rows[id], q.err
}
func nativeManagedResult() *leapmuxv1.InspectNativeAgentProcessesResponse {
	result := &leapmuxv1.InspectNativeAgentProcessesResponse{}
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	} {
		group := &leapmuxv1.NativeAgentServiceGroup{AgentProvider: provider, Available: true}
		for _, id := range []string{"live-native", "pending-native", "child-native", "external-native", "inactive-native"} {
			group.Sessions = append(group.Sessions, &leapmuxv1.NativeAgentSession{SessionId: id})
		}
		result.ServiceGroups = append(result.ServiceGroups, group)
	}
	return result
}
func TestNativeServiceExclusionUsesOnlyLiveOwnershipAndItsChildren(t *testing.T) {
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	queries := &fakeNativeManagedQueries{rows: map[string][]db.ListAgentTreeNativeSessionIdentitiesRow{
		"live-root": {
			{AgentProvider: provider, AgentSessionID: "live-native", PendingResumeSessionID: "pending-native"},
			{AgentProvider: provider, AgentSessionID: "child-native"},
		},
		"inactive-root": {{AgentProvider: provider, AgentSessionID: "inactive-native"}},
	}}
	result := nativeManagedResult()
	excludeManagedNativeServiceSessions(t.Context(), queries, func() []string { return []string{"live-root"} }, result)
	assert.Equal(t, []string{"live-root"}, queries.calls)
	require.Len(t, result.ServiceGroups, 2)
	ids := []string{}
	for _, session := range result.ServiceGroups[0].Sessions {
		ids = append(ids, session.SessionId)
	}
	assert.Equal(t, []string{"external-native", "inactive-native"}, ids)
	assert.Len(t, result.ServiceGroups[1].Sessions, 5, "another provider's same IDs remain external")
}
func TestNativeServiceExclusionRechecksRuntimeOwnershipAfterMetadataRead(t *testing.T) {
	queries := &fakeNativeManagedQueries{rows: map[string][]db.ListAgentTreeNativeSessionIdentitiesRow{
		"ended": {{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, AgentSessionID: "live-native"}},
	}}
	reads := 0
	live := func() []string {
		reads++
		if reads == 1 {
			return []string{"ended"}
		}
		return nil
	}
	result := nativeManagedResult()
	excludeManagedNativeServiceSessions(t.Context(), queries, live, result)
	assert.Len(t, result.ServiceGroups[0].Sessions, 5)
	assert.True(t, result.ServiceGroups[0].Available)
	result = nativeManagedResult()
	queries.calls = nil
	excludeManagedNativeServiceSessions(t.Context(), queries, func() []string { return nil }, result)
	assert.Empty(t, queries.calls)
	assert.Len(t, result.ServiceGroups[0].Sessions, 5, "inactive open tabs do not hide app sessions")
}
func TestNativeServiceExclusionFailsClosedOnUnavailableOrOversizedOwnership(t *testing.T) {
	for _, name := range []string{"query error", "oversized", "canceled"} {
		t.Run(name, func(t *testing.T) {
			queries := &fakeNativeManagedQueries{rows: map[string][]db.ListAgentTreeNativeSessionIdentitiesRow{}}
			ctx := t.Context()
			switch name {
			case "query error":
				queries.err = errors.New("private DB error")
			case "oversized":
				queries.rows["live"] = make([]db.ListAgentTreeNativeSessionIdentitiesRow, maxNativeManagedIdentities+1)
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			result := nativeManagedResult()
			excludeManagedNativeServiceSessions(ctx, queries, func() []string { return []string{"live"} }, result)
			for _, group := range result.ServiceGroups {
				assert.False(t, group.Available)
				assert.True(t, group.Partial)
				assert.Empty(t, group.Sessions)
			}
		})
	}
}
