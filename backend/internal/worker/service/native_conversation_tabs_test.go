package service

import (
	"context"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func nativeConversationPayloadFor(dir string) *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{
		WorkingDir: dir,
		Kind: &leapmuxv1.TabPayload_NativeConversation{NativeConversation: &leapmuxv1.NativeConversationTabPayload{
			SourceWorkerId: "source-mac", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			SessionId: "00000000-0000-4000-8000-000000000001", DisplayName: "Generated native chat",
		}},
	}
}

func TestNativeConversationTabPayloadValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		change func(*leapmuxv1.NativeConversationTabPayload)
	}{
		{"missing source", func(p *leapmuxv1.NativeConversationTabPayload) { p.SourceWorkerId = "" }},
		{"source path", func(p *leapmuxv1.NativeConversationTabPayload) { p.SourceWorkerId = "../source" }},
		{"unbounded source", func(p *leapmuxv1.NativeConversationTabPayload) { p.SourceWorkerId = strings.Repeat("a", 129) }},
		{"unsupported provider", func(p *leapmuxv1.NativeConversationTabPayload) { p.AgentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI }},
		{"missing session", func(p *leapmuxv1.NativeConversationTabPayload) { p.SessionId = "" }},
		{"session path", func(p *leapmuxv1.NativeConversationTabPayload) { p.SessionId = "../../chat.jsonl" }},
		{"unbounded session", func(p *leapmuxv1.NativeConversationTabPayload) { p.SessionId = strings.Repeat("a", 129) }},
		{"invalid UTF8", func(p *leapmuxv1.NativeConversationTabPayload) { p.DisplayName = string([]byte{0xff}) }},
		{"multiline title", func(p *leapmuxv1.NativeConversationTabPayload) { p.DisplayName = "one\ntwo" }},
		{"unbounded title", func(p *leapmuxv1.NativeConversationTabPayload) { p.DisplayName = strings.Repeat("a", maxNativeSessionDisplayNameRunes+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := nativeConversationPayloadFor("")
			tc.change(payload.GetNativeConversation())
			_, err := tabPayloadType(payload)
			require.Error(t, err)
		})
	}
	for _, provider := range []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE} {
		payload := nativeConversationPayloadFor("")
		payload.GetNativeConversation().AgentProvider = provider
		kind, err := tabPayloadType(payload)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_NATIVE_CONVERSATION, kind)
	}
}

func TestNativeConversationTabPrivateEventsRequireAgentRead(t *testing.T) {
	t.Parallel()
	event := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabPayloadRegistered{
		TabPayloadRegistered: &leapmuxv1.TabPayloadRegistered{TabId: "native-link", Payload: nativeConversationPayloadFor("")},
	}}
	fileOnly := channel.Caller{UserID: userid.MustNew("user-1"), Scopes: mustScopes("file:read")}
	agentReader := channel.Caller{UserID: userid.MustNew("user-1"), Scopes: mustScopes("file:read agent:read")}
	assert.False(t, privateEventVisible(fileOnly, event))
	assert.True(t, privateEventVisible(agentReader, event))
	assert.False(t, callerReadsTabType(fileOnly, leapmuxv1.TabType_TAB_TYPE_NATIVE_CONVERSATION))
}

func TestNativeConversationTabRoundTripDoesNotOwnAWorktree(t *testing.T) {
	t.Parallel()
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "native-link")
	ctx := context.Background()
	payload := nativeConversationPayloadFor(fx.wtDir)
	agentsBefore, err := fx.svc.Queries.ListAllAgentIDs(ctx)
	require.NoError(t, err)
	require.NoError(t, fx.svc.TabPayloads.Register(ctx, RegisterTabPayloadParams{UserID: "user-1", TabID: "native-link", Payload: payload}))
	got, err := fx.svc.TabPayloads.Get(ctx, "user-1", "native-link")
	require.NoError(t, err)
	assert.True(t, proto.Equal(payload, got), "destination placement and original source identity round-trip unchanged")
	_, err = fx.svc.TabPayloads.Get(ctx, "another-owner", "native-link")
	assert.ErrorIs(t, err, ErrTabPayloadNotFound)
	kind, err := fx.svc.TabPayloads.TabTypeOf(ctx, "user-1", "native-link")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_NATIVE_CONVERSATION, kind)
	agentsAfter, err := fx.svc.Queries.ListAllAgentIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, agentsBefore, agentsAfter, "link registration does not create or resume a managed agent")
	assert.Empty(t, tabPayloadLinkDir(got))
	count, err := fx.svc.Queries.CountWorktreeTabs(ctx, fx.wtID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "only the real agent owns a worktree reference")
	live, err := fx.svc.liveWorktreeTabs(ctx)
	require.NoError(t, err)
	for _, tab := range live {
		assert.NotEqual(t, "native-link", tab.key.tabID, "placement is absent from physical deletion blockers")
	}
	inspection, err := fx.svc.inspectLastTabClose(ctx, leapmuxv1.TabType_TAB_TYPE_NATIVE_CONVERSATION, "native-link", "user-1")
	require.NoError(t, err)
	assert.False(t, inspection.ShouldPrompt)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE, inspection.Target)
	resumed, err := fx.svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, []*leapmuxv1.TabRef{{TabType: kind, TabId: "native-link", UserId: "user-1"}})
	require.NoError(t, err)
	assert.Empty(t, resumed, "archiving a reference has no source-agent lifecycle effect")
	_, err = fx.svc.TabPayloads.Get(ctx, "user-1", "native-link")
	require.NoError(t, err, "archival retains the durable link")

	// A native link must not prevent the real last owner deleting its worktree.
	result, _ := fx.svc.closeAgentTabCommon("user-1", fx.tabID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.WorktreeRemoval)
	assert.NoDirExists(t, fx.wtDir)
	_, err = fx.svc.TabPayloads.Get(ctx, "user-1", "native-link")
	require.NoError(t, err, "the chat reference survives a removed placement checkout")
}

func TestNativeConversationTabRemoveOnlyUnlinks(t *testing.T) {
	t.Parallel()
	for _, closeKind := range []string{"revoke RPC", "workspace cleanup", "orphan reconciliation"} {
		t.Run(closeKind, func(t *testing.T) {
			fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "native-unlink")
			ctx := context.Background()
			require.NoError(t, fx.svc.TabPayloads.Register(ctx, RegisterTabPayloadParams{UserID: "user-1", TabID: "native-link", Payload: nativeConversationPayloadFor(fx.wtDir)}))
			// Even a stray legacy/malformed association cannot authorize removal.
			require.NoError(t, fx.svc.Queries.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: fx.wtID, TabType: leapmuxv1.TabType_TAB_TYPE_NATIVE_CONVERSATION, TabID: "native-link", UserID: "user-1"}))
			switch closeKind {
			case "revoke RPC":
				dispatch(fx.d, "RevokeTabPayload", &leapmuxv1.RevokeTabPayloadRequest{TabId: "native-link", WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE}, fx.w)
				require.Empty(t, fx.w.errors)
				require.Len(t, fx.w.responses, 1)
				var response leapmuxv1.RevokeTabPayloadResponse
				require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &response))
				assert.Empty(t, response.GetResult().GetFailureMessage())
				assert.NotEqual(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, response.GetResult().GetWorktreeRemoval())
			case "workspace cleanup":
				fx.svc.closeTabForDeletedWorkspace(leapmuxv1.TabType_TAB_TYPE_NATIVE_CONVERSATION, "user-1", "native-link")
			case "orphan reconciliation":
				fx.svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_NATIVE_CONVERSATION, "user-1", "native-link")
			}
			_, err := fx.svc.TabPayloads.Get(ctx, "user-1", "native-link")
			assert.ErrorIs(t, err, ErrTabPayloadNotFound)
			assert.DirExists(t, fx.wtDir)
			original, err := fx.svc.Queries.GetAgentByID(ctx, fx.tabID)
			require.NoError(t, err)
			assert.False(t, original.ClosedAt.Valid, "removing a link never closes an agent")
			count, err := fx.svc.Queries.CountWorktreeTabs(ctx, fx.wtID)
			require.NoError(t, err)
			assert.EqualValues(t, 1, count, "only the stray native association was removed")
		})
	}
}
