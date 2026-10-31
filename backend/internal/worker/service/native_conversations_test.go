package service

import (
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/require"
)

func TestNativeConversationQueryRejectsUnscopedInputs(t *testing.T) {
	for _, req := range []*leapmuxv1.ReadNativeConversationRequest{
		nil, {}, {SessionId: "../other"}, {SessionId: `C:\other`},
		{SessionId: "session", Direction: "resume"},
		{SessionId: "session", Direction: "after"},
		{SessionId: "session", Direction: "latest", Cursor: "arbitrary-path"},
		{SessionId: "session", Limit: -1}, {SessionId: "session", Limit: 201},
	} {
		_, err := nativeConversationQuery(req)
		require.Error(t, err)
	}
	query, err := nativeConversationQuery(&leapmuxv1.ReadNativeConversationRequest{SessionId: "session"})
	require.NoError(t, err)
	require.Equal(t, "latest", query.Direction)
	require.Equal(t, 100, query.Limit)
	query, err = nativeConversationQuery(&leapmuxv1.ReadNativeConversationRequest{SessionId: "session", Direction: "before", Cursor: "opaque", Limit: 20})
	require.NoError(t, err)
	require.Equal(t, "opaque", query.Cursor)
}

func TestNativeConversationResponsePreservesRawRecords(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"content":"generated harmless marker"}}`)
	page := agent.NativeConversationPage{
		SessionID: "session", Format: "test-native-format", Revision: "generation", ObservedAt: time.Now(),
		OlderCursor: "before", NewerCursor: "after", HasOlder: true,
		Records: []agent.NativeConversationRecord{{ID: "row-1", Seq: 1, NativeJSON: raw}},
	}
	response, err := nativeConversationResponse("session", page)
	require.NoError(t, err)
	require.Equal(t, raw, response.Records[0].NativeJson)
	require.Equal(t, "after", response.NewerCursor)
	require.True(t, response.HasOlder)
	page.SessionID = "another-session"
	_, err = nativeConversationResponse("session", page)
	require.Error(t, err)
}

func TestNativeConversationResponseRejectsInvalidOrDuplicateRecords(t *testing.T) {
	for _, records := range [][]agent.NativeConversationRecord{
		{{ID: "row", Seq: 1, NativeJSON: []byte(`not-json`)}},
		{{ID: "row", Seq: 0, NativeJSON: []byte(`{}`)}},
		{{ID: "row", Seq: 2, NativeJSON: []byte(`{}`)}, {ID: "row-2", Seq: 1, NativeJSON: []byte(`{}`)}},
		{{ID: "row", Seq: 1, NativeJSON: []byte(`{}`)}, {ID: "row", Seq: 2, NativeJSON: []byte(`{}`)}},
	} {
		_, err := nativeConversationResponse("session", agent.NativeConversationPage{SessionID: "session", Records: records})
		require.Error(t, err)
	}
}
