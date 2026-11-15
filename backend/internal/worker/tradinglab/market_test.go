package tradinglab

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// liveMarketData points the harness's lab at the fake broker for market data.
func liveMarketData(t *testing.T, h *labHarness) {
	t.Helper()
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	s.MarketData = leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_ROBINHOOD
	_, err = h.lab.SaveSettings(h.ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	require.NoError(t, err)
}

func TestMarketToolsOfferOnlyResearchTools(t *testing.T) {
	h := newLabHarness(t)
	_, err := h.lab.MarketTools(h.ctx, userID)
	assert.ErrorIs(t, err, ErrConflict, "recorded data has no live tools")

	liveMarketData(t, h)
	tools, err := h.lab.MarketTools(h.ctx, userID)
	require.NoError(t, err)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	assert.Subset(t, names, []string{scopeTool, "get_equity_quotes", "get_option_chains", "get_option_instruments", "get_option_quotes", "get_earnings_results"})
	for _, never := range []string{"place_option_order", "review_option_order", "cancel_option_order", "place_equity_order", "add_to_watchlist",
		"get_accounts", "get_portfolio", "get_option_orders", "get_bank_accounts"} {
		assert.NotContains(t, names, never)
	}
}

func TestMarketCallForwardsResearchToolsAndLogsThem(t *testing.T) {
	h := newLabHarness(t)
	liveMarketData(t, h)

	out, isErr, err := h.lab.MarketCall(h.ctx, userID, "run-1", "task-1", "get_equity_quotes", `{"symbols": ["SYNA"]}`)
	require.NoError(t, err)
	assert.False(t, isErr)
	assert.Contains(t, out, `"symbol":"SYNA"`)

	scope, _, err := h.lab.MarketCall(h.ctx, userID, "run-1", "task-1", scopeTool, "")
	require.NoError(t, err)
	assert.Contains(t, scope, "SYNA")

	for _, tool := range []string{"place_option_order", "place_equity_order", "get_accounts", "add_to_watchlist", "no_such_tool"} {
		_, _, err := h.lab.MarketCall(h.ctx, userID, "run-1", "task-1", tool, `{"account_number": "5QR12345"}`)
		assert.ErrorIs(t, err, ErrDenied, tool)
	}
	assert.False(t, slices.ContainsFunc(h.broker.toolCalls(), func(name string) bool {
		return name == "place_option_order" || name == "place_equity_order" || name == "get_accounts" || name == "add_to_watchlist"
	}), "a refused tool never reaches the broker")

	_, _, err = h.lab.MarketCall(h.ctx, userID, "run-1", "task-1", "get_equity_quotes", `not json`)
	assert.ErrorIs(t, err, ErrInvalid)

	var n int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM trading_market_calls WHERE run_id = 'run-1' AND task_id = 'task-1' AND tool = 'get_equity_quotes'`).Scan(&n))
	assert.Equal(t, 1, n, "the call is in the task's log")
}

func TestSnapshotFromASelectionTakesEveryPriceFromTheBroker(t *testing.T) {
	h := newLabHarness(t)
	liveMarketData(t, h)
	snap, _ := snapshotOf(t, h.frames[0])
	call := liquidCall(t, snap)
	var put *OptionQuote
	for i := range snap.Options {
		if o := &snap.Options[i]; o.Underlying == "SYNB" && o.Right == "put" && len(executionProblems(o, rules())) == 0 {
			put = o
			break
		}
	}
	require.NotNil(t, put)

	// The research step looked up earnings, so a catalyst citing that tool
	// stays; one citing a tool it never called is dropped.
	_, _, err := h.lab.MarketCall(h.ctx, userID, "run-1", "task-gather", "get_earnings_results", `{"symbol": "SYNA"}`)
	require.NoError(t, err)
	selection, err := json.Marshal(map[string]any{
		"symbols":   []string{"SYNA", "SYNB"},
		"contracts": []any{map[string]any{"instrument_id": instrumentID(call.OCCSymbol)}, map[string]any{"instrument_id": instrumentID(put.OCCSymbol)}},
		"catalysts": []any{
			map[string]any{"symbol": "SYNA", "type": "earnings", "date": "2026-11-05", "title": "Q4 earnings", "tool": "get_earnings_results"},
			map[string]any{"symbol": "SYNB", "type": "fda", "date": "2026-11-05", "title": "An invented approval", "tool": "get_news"},
		},
	})
	require.NoError(t, err)

	res, err := h.lab.snapshotExecutor(h.ctx, workflow.ExecutorRequest{
		UserID: userID, Run: &leapmuxv1.WorkflowRun{Id: "run-1"}, Task: &leapmuxv1.WorkflowTask{Id: "task-snapshot"},
		Step:   &leapmuxv1.WorkflowStep{Key: "snapshot", Params: map[string]string{"selection_step": "gather"}},
		Frozen: []*leapmuxv1.WorkflowFrozenOutput{{StepKey: "gather", CanonicalJson: string(selection)}},
	})
	require.NoError(t, err)
	got, err := DecodeSnapshot(res.StructuredOutput)
	require.NoError(t, err)

	var occs []string
	for _, o := range got.Options {
		occs = append(occs, o.OCCSymbol)
		want := snap.Contract(o.OCCSymbol)
		require.NotNil(t, want)
		assert.Equal(t, want.Bid, o.Bid, "the bid is the broker's")
		assert.Equal(t, want.Ask, o.Ask, "the ask is the broker's")
	}
	assert.ElementsMatch(t, []string{call.OCCSymbol, put.OCCSymbol}, occs)
	var titles []string
	for _, c := range got.Catalysts {
		titles = append(titles, c.Title)
	}
	assert.Contains(t, titles, "Q4 earnings")
	assert.NotContains(t, titles, "An invented approval", "a catalyst whose tool was never called is dropped")
	assert.Contains(t, h.broker.toolCalls(), "get_option_quotes")
}

// On a closed market the snapshot still asks Robinhood for every quote, and
// keeps the last session's as allowed but not preferred.
func TestSnapshotOnAClosedMarketStillQuotesFromTheBroker(t *testing.T) {
	h := newLabHarness(t)
	liveMarketData(t, h)
	snap, _ := snapshotOf(t, h.frames[0])
	sunday := nextSunday(h.frames[0].AsOf)
	_, last := MarketSession(sunday)
	var ids []any
	for i := range snap.Options {
		o := &snap.Options[i]
		if exp, _ := time.Parse(time.DateOnly, o.Expiration); exp.After(sunday) && len(executionProblems(o, rules())) == 0 && len(ids) < 2 {
			ids = append(ids, map[string]any{"instrument_id": instrumentID(o.OCCSymbol)})
		}
	}
	require.NotEmpty(t, ids)
	selection, err := json.Marshal(map[string]any{"symbols": []string{"SYNA", "SYNB"}, "contracts": ids})
	require.NoError(t, err)
	h.clock.Set(sunday)
	h.broker.quoteTime = last.Close.Add(-time.Minute)
	before := len(h.broker.toolCalls())

	res, err := h.lab.snapshotExecutor(h.ctx, workflow.ExecutorRequest{
		UserID: userID, Run: &leapmuxv1.WorkflowRun{Id: "run-1"}, Task: &leapmuxv1.WorkflowTask{Id: "task-snapshot"},
		Step:   &leapmuxv1.WorkflowStep{Key: "snapshot", Params: map[string]string{"selection_step": "gather"}},
		Frozen: []*leapmuxv1.WorkflowFrozenOutput{{StepKey: "gather", CanonicalJson: string(selection)}},
	})
	require.NoError(t, err)
	got, err := DecodeSnapshot(res.StructuredOutput)
	require.NoError(t, err)
	assert.True(t, got.Freshness.MarketClosed)
	assert.Len(t, got.Options, len(ids))
	for _, o := range got.Options {
		assert.True(t, o.LastSession, o.OCCSymbol)
	}
	calls := h.broker.toolCalls()[before:]
	assert.Contains(t, calls, "get_equity_quotes", "underlyings are asked for even with the market closed")
	assert.Contains(t, calls, "get_option_quotes", "contracts are asked for even with the market closed")
}
