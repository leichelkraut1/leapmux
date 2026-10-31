package db_test

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentTreeNativeSessionIdentitiesExcludeClosedHistoryAndUnrelatedTabs(t *testing.T) {
	store, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), store))
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	for _, row := range []struct {
		id, parent, session, pending string
		closed                       bool
	}{
		{"live", "", "live-native", "pending-native", false},
		{"child", "live", "child-native", "", false},
		{"grandchild", "child", "grandchild-native", "", false},
		{"closed-child", "live", "closed-native", "", true},
		{"orphan", "closed-child", "orphan-native", "", false},
		{"inactive", "", "inactive-native", "", false},
		{"closed-root", "", "history-native", "", true},
	} {
		var parent any
		if row.parent != "" {
			parent = row.parent
		}
		var closed any
		if row.closed {
			closed = "2026-10-28T00:00:00.000Z"
		}
		_, err := store.ExecContext(t.Context(), `INSERT INTO agents
   (id,parent_agent_id,agent_provider,agent_session_id,pending_resume_session_id,closed_at)
   VALUES (?,?,?,?,?,?)`, row.id, parent, provider, row.session, row.pending, closed)
		require.NoError(t, err)
	}
	queries := db.New(store)
	rows, err := queries.ListAgentTreeNativeSessionIdentities(t.Context(), "live")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.ID)
		assert.Equal(t, provider, row.AgentProvider)
		if row.ID == "live" {
			assert.Equal(t, "live-native", row.AgentSessionID)
			assert.Equal(t, "pending-native", row.PendingResumeSessionID)
		}
	}
	assert.ElementsMatch(t, []string{"live", "child", "grandchild"}, ids)
	rows, err = queries.ListAgentTreeNativeSessionIdentities(t.Context(), "closed-root")
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = queries.ListAgentTreeNativeSessionIdentities(t.Context(), "missing")
	require.NoError(t, err)
	assert.Empty(t, rows)
}
