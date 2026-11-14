package tradinglab

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestLabConnectorPassphrase(t *testing.T) {
	h := newLabHarness(t)
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	assert.False(t, s.GetHasConnectorPassphrase())
	assert.ErrorIs(t, h.lab.CheckConnectorPassphrase(h.ctx, userID, "anything at all"), ErrPassphrase)

	save := func(req *leapmuxv1.SaveTradingLabSettingsRequest) (*leapmuxv1.TradingLabSettings, error) {
		req.Settings = s
		return h.lab.SaveSettings(h.ctx, userID, req)
	}
	_, err = save(&leapmuxv1.SaveTradingLabSettingsRequest{NewConnectorPassphrase: "correct horse battery"})
	assert.ErrorIs(t, err, ErrInvalid, "the confirmation passphrase cannot double as the connector's")
	_, err = save(&leapmuxv1.SaveTradingLabSettingsRequest{NewConnectorPassphrase: "short"})
	assert.ErrorIs(t, err, ErrInvalid)

	out, err := save(&leapmuxv1.SaveTradingLabSettingsRequest{NewConnectorPassphrase: "phone sign-in words"})
	require.NoError(t, err)
	assert.True(t, out.GetHasConnectorPassphrase())
	assert.True(t, out.GetHasConfirmationPassphrase())
	require.NoError(t, h.lab.CheckConnectorPassphrase(h.ctx, userID, "phone sign-in words"))
	assert.ErrorIs(t, h.lab.CheckConnectorPassphrase(h.ctx, userID, "correct horse battery"), ErrPassphrase)

	// Saving other settings keeps it, and the stored settings never carry it.
	out, err = save(&leapmuxv1.SaveTradingLabSettingsRequest{})
	require.NoError(t, err)
	assert.True(t, out.GetHasConnectorPassphrase())
	stored, _, err := getSettings(h.ctx, h.db, userID)
	require.NoError(t, err)
	assert.False(t, stored.GetHasConnectorPassphrase())
}

func TestLabSettingsShowTheConnectorURLOnlyWhenServed(t *testing.T) {
	h := newLabHarness(t)
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, s.GetConnectorUrl())

	served := New(Config{DB: h.db, Clock: h.clock, ConnectorURL: "https://lab.example.com/lab-mcp"})
	s, err = served.Settings(h.ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "https://lab.example.com/lab-mcp", s.GetConnectorUrl())
}

func TestLabRunIDsListsSnapshotRunsNewestFirst(t *testing.T) {
	h := newLabHarness(t)
	for i, run := range []string{"run-a", "run-b"} {
		_, err := h.db.ExecContext(h.ctx, `INSERT INTO trading_snapshots (id, user_id, run_id, task_id, sha256, as_of_ms, source, canonical_json, captured_at_ms)
			VALUES (?, ?, ?, ?, ?, 1, 1, '{}', ?)`, "snap-"+run, userID, run, "task-"+run, "0000000000000000000000000000000000000000000000000000000000000000", int64(i+1))
		require.NoError(t, err)
	}
	ids, err := h.lab.RunIDs(h.ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, []string{"run-b", "run-a"}, ids)
	other, err := h.lab.RunIDs(h.ctx, "someone-else")
	require.NoError(t, err)
	assert.Empty(t, other)
}
