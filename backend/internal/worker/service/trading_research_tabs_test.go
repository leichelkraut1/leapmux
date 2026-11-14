package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func tradingResearchPayload() *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_TradingResearch{TradingResearch: &leapmuxv1.TradingResearchTabPayload{}}}
}

// A Trading research tab is a link to the worker's own lab: it registers,
// survives archiving and a reconnect's report, and closing it drops only its
// row, never a worktree.
func TestTradingResearchTabIsALinkThatOwnsNoWorktree(t *testing.T) {
	t.Parallel()
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "trading-tab")
	ctx := context.Background()
	require.NoError(t, fx.svc.TabPayloads.Register(ctx, RegisterTabPayloadParams{UserID: "user-1", TabID: "trading-1", Payload: tradingResearchPayload()}))
	got, err := fx.svc.TabPayloads.Get(ctx, "user-1", "trading-1")
	require.NoError(t, err)
	assert.True(t, proto.Equal(tradingResearchPayload(), got))
	kind, err := fx.svc.TabPayloads.TabTypeOf(ctx, "user-1", "trading-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TRADING_RESEARCH, kind)
	assert.Empty(t, tabPayloadLinkDir(got))
	assert.True(t, isLinkTab(kind))

	inspection, err := fx.svc.inspectLastTabClose(ctx, kind, "trading-1", "user-1")
	require.NoError(t, err)
	assert.False(t, inspection.ShouldPrompt)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE, inspection.Target)

	_, err = fx.svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{{TabType: kind, TabId: "trading-1", UserId: "user-1"}})
	require.NoError(t, err)

	fx.svc.CloseTabForReconcile(kind, "user-1", "trading-1")
	_, err = fx.svc.TabPayloads.Get(ctx, "user-1", "trading-1")
	assert.ErrorIs(t, err, ErrTabPayloadNotFound)
	assert.DirExists(t, fx.wtDir, "closing the tab touches no worktree")
	count, err := fx.svc.Queries.CountWorktreeTabs(ctx, fx.wtID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "only the real agent holds a worktree reference")
}
