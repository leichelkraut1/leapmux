package tradinglab

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

// A Monday, so the recording starts on a trading day.
var sampleStart = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

func sampleReplay(t *testing.T) (string, []*Frame) {
	t.Helper()
	dir := t.TempDir()
	_, err := GenerateSampleReplay(dir, sampleStart, 12, 7)
	require.NoError(t, err)
	files, err := (&Replay{Dir: dir}).load()
	require.NoError(t, err)
	var frames []*Frame
	for _, f := range files {
		frames = append(frames, f.frame)
	}
	return dir, frames
}

func rules() *leapmuxv1.TradingSimulationRules { return DefaultSettings().GetSimulation() }

func snapshotOf(t *testing.T, f *Frame) (*Snapshot, string) {
	t.Helper()
	snap, err := BuildSnapshot(f, "replay", Provenance{Provider: "replay"}, SnapshotOptions{
		Symbols: SampleSymbols, MaxDTE: 14, StrikesPerSide: 6, MaxQuoteAge: 15 * time.Minute, MaxSnapshotAge: 15 * time.Minute, Now: f.AsOf,
	})
	require.NoError(t, err)
	_, sum, err := snap.Canonical()
	require.NoError(t, err)
	return snap, sum
}

// liquidCall picks an executable near-the-money call on SYNA.
func liquidCall(t *testing.T, snap *Snapshot) *OptionQuote {
	t.Helper()
	under := snap.Underlying("SYNA").Last
	var best *OptionQuote
	for i := range snap.Options {
		o := &snap.Options[i]
		if o.Underlying != "SYNA" || o.Right != "call" || len(executionProblems(o, rules())) > 0 {
			continue
		}
		if best == nil || math.Abs(o.Strike-under) < math.Abs(best.Strike-under) {
			best = o
		}
	}
	require.NotNil(t, best)
	return best
}

// goodRecommendation is internally consistent with the snapshot.
func goodRecommendation(snap *Snapshot, q *OptionQuote) Recommendation {
	var r Recommendation
	limit := round2(q.Ask + slip(rules()))
	r.ID, r.Underlying, r.Direction, r.Strategy = "r1", q.Underlying, "bullish", "long_call"
	r.Contract.OCCSymbol, r.Contract.Expiration, r.Contract.Strike, r.Contract.Right = q.OCCSymbol, q.Expiration, q.Strike, q.Right
	r.Entry.LimitPrice = limit
	r.Thesis = "Earnings inside the horizon with implied volatility below realized."
	r.Catalyst.Type, r.Catalyst.Description = "earnings", "Quarterly report"
	r.Evidence = append(r.Evidence, struct {
		Claim  string `json:"claim"`
		Source string `json:"source"`
	}{Claim: "The call is liquid", Source: "options[0]"})
	for _, s := range []struct {
		name string
		p    float64
	}{{"up", 0.3}, {"flat", 0.4}, {"down", 0.3}} {
		r.Scenarios = append(r.Scenarios, struct {
			Name            string  `json:"name"`
			Probability     float64 `json:"probability"`
			UnderlyingPrice float64 `json:"underlying_price"`
			OptionValue     float64 `json:"option_value"`
		}{Name: s.name, Probability: s.p, UnderlyingPrice: 100, OptionValue: 1})
	}
	r.Probabilities.Profit, r.Probabilities.Reach3x, r.Probabilities.Reach5x = 0.3, 0.1, 0.04
	r.Risk.MaxLossPerContract = round2(limit*100 + fee(rules()))
	r.Exits.TakeProfit = append(r.Exits.TakeProfit, struct {
		Price    float64 `json:"price"`
		Fraction float64 `json:"fraction"`
	}{Price: round2(limit * 3), Fraction: 0.5})
	r.Exits.StopLoss = round2(limit / 2)
	r.Exits.TimeStop = snap.AsOf.AddDate(0, 0, 2).Format(time.DateOnly)
	r.Invalidation = []string{"SYNA closes below its 20-day low"}
	r.HorizonDays = 2
	return r
}

func TestOCCSymbolRoundTrip(t *testing.T) {
	occ, err := OCCSymbol("XYZ", "2026-11-20", "call", 110)
	require.NoError(t, err)
	assert.Equal(t, "XYZ   261120C00110000", occ)
	p, err := ParseOCC("XYZ261120P00012500")
	require.NoError(t, err)
	assert.Equal(t, ParsedOCC{Root: "XYZ", Expiration: "2026-11-20", Right: "put", Strike: 12.5}, p)
	norm, err := NormalizeOCC("XYZ261120C00110000")
	require.NoError(t, err)
	assert.Equal(t, occ, norm)
	_, err = ParseOCC("XYZ261120X00110000")
	assert.Error(t, err)
}

func TestSnapshotIsDeterministicAndDropsStaleOrMissingQuotes(t *testing.T) {
	_, frames := sampleReplay(t)
	f := frames[0]
	snapA, sumA := snapshotOf(t, f)

	shuffled := *f
	shuffled.Options = slices.Clone(f.Options)
	slices.Reverse(shuffled.Options)
	shuffled.Underlyings = slices.Clone(f.Underlyings)
	slices.Reverse(shuffled.Underlyings)
	_, sumB := snapshotOf(t, &shuffled)
	assert.Equal(t, sumA, sumB, "input order does not change the hash")
	assert.Equal(t, 2*6*2*2*len(SampleSymbols), len(snapA.Options), "six strikes each side, both rights, the two weekly expiries within 14 days, per symbol")

	stale := *f
	stale.Options = slices.Clone(f.Options)
	stale.Options[0].QuoteTime = f.AsOf.Add(-time.Hour)
	stale.Options[1].Bid, stale.Options[1].Ask = 0, 0
	snap, _ := snapshotOf(t, &stale)
	occ0, _ := NormalizeOCC(f.Options[0].OCCSymbol)
	occ1, _ := NormalizeOCC(f.Options[1].OCCSymbol)
	assert.Nil(t, snap.Contract(occ0))
	assert.Nil(t, snap.Contract(occ1))
	assert.Contains(t, snap.Freshness.StaleQuotes, occ0)
	assert.Contains(t, snap.Freshness.MissingQuotes, occ1)
}

func TestSnapshotRefusesOldFutureOrEmptyData(t *testing.T) {
	_, frames := sampleReplay(t)
	f := frames[0]
	opts := SnapshotOptions{Symbols: SampleSymbols, MaxDTE: 14, MaxQuoteAge: time.Minute, MaxSnapshotAge: 15 * time.Minute}

	opts.Now = f.AsOf.Add(time.Hour)
	_, err := BuildSnapshot(f, "replay", Provenance{}, opts)
	assert.ErrorIs(t, err, ErrStale, "a frame an hour old is refused")

	opts.Now = f.AsOf.Add(-time.Hour)
	_, err = BuildSnapshot(f, "replay", Provenance{}, opts)
	assert.ErrorIs(t, err, ErrStale, "a frame from the future is refused")

	opts.Now = f.AsOf
	old := *f
	old.Underlyings = slices.Clone(f.Underlyings)
	for i := range old.Underlyings {
		old.Underlyings[i].QuoteTime = f.AsOf.Add(-time.Hour)
	}
	_, err = BuildSnapshot(&old, "replay", Provenance{}, opts)
	assert.ErrorIs(t, err, ErrStale, "no fresh underlying, no snapshot")
}

func TestCheckRecommendationFindsEveryInconsistency(t *testing.T) {
	_, frames := sampleReplay(t)
	snap, sum := snapshotOf(t, frames[0])
	q := liquidCall(t, snap)
	p := &Prediction{SchemaVersion: 1, SnapshotSHA256: sum}

	good := goodRecommendation(snap, q)
	assert.Empty(t, CheckRecommendation(snap, sum, p, &good, rules()))

	cases := map[string]func(r *Recommendation, p *Prediction){
		"cites another snapshot":      func(_ *Recommendation, p *Prediction) { p.SnapshotSHA256 = strings.Repeat("0", 64) },
		"a put called bullish":        func(r *Recommendation, _ *Prediction) { r.Strategy = "long_put" },
		"a contract not in snapshot":  func(r *Recommendation, _ *Prediction) { r.Contract.OCCSymbol = "SYNA  261231C00500000" },
		"a limit below the ask":       func(r *Recommendation, _ *Prediction) { r.Entry.LimitPrice = q.Bid },
		"a wrong maximum loss":        func(r *Recommendation, _ *Prediction) { r.Risk.MaxLossPerContract = 1 },
		"probabilities not summing":   func(r *Recommendation, _ *Prediction) { r.Scenarios[0].Probability = 0.9 },
		"5x likelier than 3x":         func(r *Recommendation, _ *Prediction) { r.Probabilities.Reach5x = 0.5 },
		"a stop above the entry":      func(r *Recommendation, _ *Prediction) { r.Exits.StopLoss = 99 },
		"a time stop after expiry":    func(r *Recommendation, _ *Prediction) { r.Exits.TimeStop = "2030-01-01" },
		"a horizon outliving expiry":  func(r *Recommendation, _ *Prediction) { r.HorizonDays = 40 },
		"a take-profit below entry":   func(r *Recommendation, _ *Prediction) { r.Exits.TakeProfit[0].Price = 0.01 },
		"a catalyst after its expiry": func(r *Recommendation, _ *Prediction) { r.Catalyst.Date = "2030-01-01" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := goodRecommendation(snap, q)
			pp := *p
			mutate(&r, &pp)
			assert.NotEmpty(t, CheckRecommendation(snap, sum, &pp, &r, rules()))
		})
	}
}

func TestRubricScoresIdenticalSubmissionsIdentically(t *testing.T) {
	scores := map[string]int{"evidence": 4, "pricing": 3, "catalyst": 5, "risk_reward": 2, "calibration": 3, "executability": 4}
	total, err := RubricScore(scores)
	require.NoError(t, err)
	assert.InDelta(t, 69.0, total, 1e-9, "(0.8+0.6+0.75+0.4+0.3+0.6)/5")
	_, err = RubricScore(map[string]int{"evidence": 4})
	assert.Error(t, err, "an incomplete score card is not a low score")
	_, err = RubricScore(map[string]int{"evidence": 9, "pricing": 3, "catalyst": 5, "risk_reward": 2, "calibration": 3, "executability": 4})
	assert.Error(t, err)

	pred := &Prediction{Recommendations: []Recommendation{{ID: "r1"}}}
	other := &Prediction{Recommendations: []Recommendation{{ID: "r1"}, {ID: "r2"}}}
	var e Evaluation
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":1,"rubric_version":"options-v1","submissions":[
		{"label":"Submission A","recommendations":[{"recommendation_id":"r1","scores":{"evidence":4,"pricing":3,"catalyst":5,"risk_reward":2,"calibration":3,"executability":4},"verdict":"acceptable","notes":""}]},
		{"label":"Submission B","recommendations":[{"recommendation_id":"r1","scores":{"evidence":4,"pricing":3,"catalyst":5,"risk_reward":2,"calibration":3,"executability":4},"verdict":"acceptable","notes":""}]}]}`), &e))
	got, _, problems := EvaluationScores(&e, map[string]*Prediction{"Submission A": pred, "Submission B": other})
	assert.Equal(t, got["Submission A/r1"], got["Submission B/r1"], "the same scores total the same under either label")
	assert.Equal(t, []string{"the evaluation did not score Submission B/r2"}, problems)
}

func TestShadowFillsAtTheAskPlusSlippageNeverTheMidpoint(t *testing.T) {
	_, frames := sampleReplay(t)
	snap, _ := snapshotOf(t, frames[0])
	q := liquidCall(t, snap)
	r := rules()
	pos := OpenShadow(leapmuxv1.TradingBook_TRADING_BOOK_BLIND, snap, q.OCCSymbol, q.Ask+1, r)
	require.Equal(t, leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN, pos.GetState(), pos.GetNote())
	assert.InDelta(t, q.Ask+0.02, pos.GetEntryPrice(), 1e-9, "a buy pays the ask plus slippage")
	assert.Greater(t, pos.GetEntryPrice(), (q.Bid+q.Ask)/2)
	perContract := pos.GetEntryPrice()*100 + r.GetFeePerContract()
	assert.Equal(t, uint32(math.Floor(r.GetAllocationPerIdea()/perContract)), pos.GetQuantity())
	assert.InDelta(t, perContract*float64(pos.GetQuantity()), pos.GetCost(), 0.01)
	assert.Less(t, pos.GetLastMark(), pos.GetCost(), "marked at the bid, a fresh position is already down the spread")

	assert.Equal(t, leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_NOT_FILLED,
		OpenShadow(leapmuxv1.TradingBook_TRADING_BOOK_BLIND, snap, q.OCCSymbol, q.Bid, r).GetState(), "a limit below the ask does not fill")
	assert.Equal(t, leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_NOT_FILLED,
		OpenShadow(leapmuxv1.TradingBook_TRADING_BOOK_BLIND, snap, "SYNA  261231C00500000", 1, r).GetState())
	wide := proto.Clone(r).(*leapmuxv1.TradingSimulationRules)
	wide.MaxSpreadFraction = 0.0001
	assert.Contains(t, OpenShadow(leapmuxv1.TradingBook_TRADING_BOOK_BLIND, snap, q.OCCSymbol, 0, wide).GetNote(), "spread")
	small := proto.Clone(r).(*leapmuxv1.TradingSimulationRules)
	small.AllocationPerIdea = 1
	assert.Contains(t, OpenShadow(leapmuxv1.TradingBook_TRADING_BOOK_BLIND, snap, q.OCCSymbol, 0, small).GetNote(), "allocated")
}

func TestInverseIsTheOtherRightAtTheNearestDelta(t *testing.T) {
	_, frames := sampleReplay(t)
	snap, _ := snapshotOf(t, frames[0])
	q := liquidCall(t, snap)
	inv, err := InverseContract(snap, q.OCCSymbol)
	require.NoError(t, err)
	assert.Equal(t, "put", inv.Right)
	assert.Equal(t, q.Expiration, inv.Expiration)
	for _, o := range snap.Options {
		if o.Underlying == q.Underlying && o.Expiration == q.Expiration && o.Right == "put" {
			assert.LessOrEqual(t, math.Abs(math.Abs(inv.Delta)-math.Abs(q.Delta)), math.Abs(math.Abs(o.Delta)-math.Abs(q.Delta))+1e-12)
		}
	}
	assert.NotEqual(t, q.Ask, 0.0)
}

func TestMarkingRecordsHorizonsMissingDataAndExpiry(t *testing.T) {
	_, frames := sampleReplay(t)
	snap, _ := snapshotOf(t, frames[0])
	q := liquidCall(t, snap)
	r := rules()
	pos := OpenShadow(leapmuxv1.TradingBook_TRADING_BOOK_BLIND, snap, q.OCCSymbol, 0, r)
	require.Equal(t, leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN, pos.GetState())

	gap := *frames[1]
	gap.Options = nil
	marks := MarkPosition(pos, &gap, r)
	require.Len(t, marks, 1)
	assert.True(t, marks[0].Missing, "no bid is recorded as missing, never as zero")
	assert.Equal(t, leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN, pos.GetState())

	for _, f := range frames[1:] {
		MarkPosition(pos, f, r)
	}
	days := []uint32{}
	for _, h := range pos.GetHorizons() {
		days = append(days, h.GetDays())
	}
	assert.Equal(t, []uint32{1, 3, 5}, days)
	assert.Equal(t, leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_EXPIRED, pos.GetState(), "the recording runs past expiry")
	assert.InDelta(t, pos.GetLastMark()-pos.GetCost(), pos.GetRealizedPnl(), 0.01)
}

func TestBookMetrics(t *testing.T) {
	r := rules()
	mk := func(id string, cost, pnl, peak, forecast float64, state leapmuxv1.TradingPositionState) *leapmuxv1.TradingPosition {
		return &leapmuxv1.TradingPosition{Id: id, Book: leapmuxv1.TradingBook_TRADING_BOOK_BLIND, Quantity: 1, Cost: cost, RealizedPnl: pnl,
			LastMark: cost + pnl, PeakMultiple: peak, ForecastProbability: forecast, State: state, OpenedAtMs: 1}
	}
	closed := leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_EXPIRED
	positions := []*leapmuxv1.TradingPosition{
		mk("a", 100, 400, 5.2, 0.6, closed),
		mk("b", 100, -100, 0.8, 0.4, closed),
		mk("c", 100, -50, 3.1, 0.5, closed),
		{Id: "d", Book: leapmuxv1.TradingBook_TRADING_BOOK_BLIND, State: leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_NOT_FILLED},
		mk("other", 100, 1000, 9, 0.9, closed),
	}
	positions[4].Book = leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN
	marks := map[string][]Mark{"a": {{AtMs: 10, Value: 50}, {AtMs: 30, Value: 500}}, "b": {{AtMs: 20, Value: 0}}, "c": {{AtMs: 20, Value: 50}}}
	m := BookMetrics(leapmuxv1.TradingBook_TRADING_BOOK_BLIND, positions, marks, r)
	assert.Equal(t, uint32(4), m.GetIdeas())
	assert.Equal(t, uint32(3), m.GetFilled())
	assert.Equal(t, uint32(1), m.GetNotExecutable())
	assert.InDelta(t, 250, m.GetNetPnl(), 1e-9)
	assert.InDelta(t, 250.0/300, m.GetReturnOnRisk(), 1e-4)
	assert.InDelta(t, 1.0/3, m.GetWinRate(), 1e-4)
	assert.InDelta(t, ((0.6-1)*(0.6-1)+0.4*0.4+0.5*0.5)/3, m.GetBrierScore(), 1e-4)
	assert.InDelta(t, 2.0/3, m.GetHit_3XRate(), 1e-4)
	assert.InDelta(t, 1.0/3, m.GetHit_5XRate(), 1e-4)
	assert.InDelta(t, 200, m.GetMaxDrawdown(), 1e-9, "a, b and c all sink to their lows at t=20 before a recovers")
}

// --- Lab, adapter and gateway ---

type labHarness struct {
	t      *testing.T
	ctx    context.Context
	db     *sql.DB
	clock  *quartz.Mock
	lab    *Lab
	broker *fakeBroker
	frames []*Frame
	dir    string
	ids    int
}

const userID = "user-1"

func newLabHarness(t *testing.T) *labHarness {
	t.Helper()
	db, err := workerdb.Open(filepath.Join(t.TempDir(), "worker.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), db))
	dir, frames := sampleReplay(t)
	h := &labHarness{t: t, ctx: t.Context(), db: db, clock: testutil.NewQuartzMock(t), frames: frames, dir: dir}
	h.clock.Set(frames[0].AsOf)
	h.broker = newFakeBroker(t, frames[0])
	t.Setenv("TEST_RH_TOKEN", "good-token")
	h.lab = New(Config{DB: db, Clock: h.clock, NewID: func() string { h.ids++; return fmt.Sprintf("id-%03d", h.ids) }})
	s := DefaultSettings()
	s.ReplayDir, s.Watchlist = dir, SampleSymbols
	s.Risk.MaxLossPerOrder, s.Risk.MaxDailyRisk, s.Risk.MaxOpenRisk = 1000, 2000, 2000
	s.Robinhood = &leapmuxv1.TradingMcpConfig{Transport: leapmuxv1.TradingMcpTransport_TRADING_MCP_TRANSPORT_HTTP, Url: h.broker.url(), TokenEnv: "TEST_RH_TOKEN"}
	_, err = h.lab.SaveSettings(h.ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	require.NoError(t, err)
	return h
}

func (h *labHarness) goLive() {
	h.t.Helper()
	st, err := h.lab.VerifyBroker(h.ctx, userID)
	require.NoError(h.t, err)
	require.True(h.t, st.GetLiveReady(), st.GetDetail())
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(h.t, err)
	s.Mode = leapmuxv1.TradingLabMode_TRADING_LAB_MODE_LIVE
	_, err = h.lab.SaveSettings(h.ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	require.NoError(h.t, err)
}

// approvedCandidate stores an approved candidate on a liquid call.
func (h *labHarness) approvedCandidate() *leapmuxv1.TradingCandidate {
	h.t.Helper()
	snap, _ := snapshotOf(h.t, h.frames[0])
	q := liquidCall(h.t, snap)
	c := &leapmuxv1.TradingCandidate{
		Id: "cand-1", RunId: "run-1", Source: leapmuxv1.TradingBook_TRADING_BOOK_BLIND, RecommendationId: "r1",
		Status: leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE, OccSymbol: q.OCCSymbol, EntryLimit: round2(q.Ask), ProbabilityProfit: 0.3,
	}
	require.NoError(h.t, putCandidate(h.ctx, h.db, userID, c, 1))
	c, err := h.lab.DecideCandidate(h.ctx, userID, &leapmuxv1.DecideTradingCandidateRequest{CandidateId: c.GetId(), Approve: true})
	require.NoError(h.t, err)
	return c
}

func TestRobinhoodToolsAreClassifiedAndAuthenticationVerified(t *testing.T) {
	h := newLabHarness(t)
	h.broker.set(func(b *fakeBroker) { b.extraTools = []string{"place_crypto_trade", "transfer_funds"} })
	st, err := h.lab.VerifyBroker(h.ctx, userID)
	require.NoError(t, err)
	assert.True(t, st.GetAuthenticated())
	assert.True(t, st.GetLiveReady(), st.GetDetail())
	assert.Equal(t, "****2345", st.GetAccountLabel(), "the account number is masked")
	assert.ElementsMatch(t, []string{"preview_option_order", "place_option_order", "cancel_option_order"}, st.GetTradingTools())
	assert.NotContains(t, st.GetReadOnlyTools(), "place_option_order")
	assert.ElementsMatch(t, []string{"place_crypto_trade", "transfer_funds"}, st.GetUnclassifiedTools(), "tools the lab does not know are never used")

	t.Setenv("TEST_RH_TOKEN", "stolen-or-expired")
	st, err = h.lab.VerifyBroker(h.ctx, userID)
	require.NoError(t, err)
	assert.False(t, st.GetAuthenticated())
	assert.Contains(t, st.GetDetail(), "not authorized")

	t.Setenv("TEST_RH_TOKEN", "good-token")
	s, _ := h.lab.Settings(h.ctx, userID)
	s.Robinhood.ToolNames = map[string]string{OpQuote: "place_crypto_trade"}
	_, err = h.lab.SaveSettings(h.ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	require.NoError(t, err)
	sess, err := ConnectRobinhood(h.ctx, s.GetRobinhood(), nil, nil)
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()
	assert.False(t, sess.Reader().Supports(OpQuote), "a read operation mapped to an order tool is refused")
	assert.False(t, sess.Reader().Supports(OpPlaceOrder), "the read-only view never reports a trading operation")
	_, isTrader := sess.Reader().(TradingBroker)
	assert.False(t, isTrader, "the read-only view cannot be turned into the trading one")
}

func TestPaperModeRefusesEveryLiveOrder(t *testing.T) {
	h := newLabHarness(t)
	c := h.approvedCandidate()
	_, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	assert.ErrorIs(t, err, ErrPaperMode)
	assert.Zero(t, h.broker.placements())

	s, _ := h.lab.Settings(h.ctx, userID)
	s.Mode = leapmuxv1.TradingLabMode_TRADING_LAB_MODE_LIVE
	_, err = h.lab.SaveSettings(h.ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	assert.ErrorIs(t, err, ErrConflict, "live mode needs a verified broker first")
}

func TestPrepareChecksTheOrderAndPlacesNothing(t *testing.T) {
	h := newLabHarness(t)
	h.goLive()
	c := h.approvedCandidate()

	o, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PREPARED, o.GetState())
	assert.Equal(t, []string{"Options can expire worthless."}, o.GetBrokerAlerts())
	assert.InDelta(t, c.GetEntryLimit()*100+0.06, o.GetMaxLoss(), 0.001)
	assert.Equal(t, orderFingerprint(o), o.GetFingerprint())
	assert.Len(t, o.GetChecks(), 4)
	assert.Zero(t, h.broker.placements(), "preparing never places")

	_, err = h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 50})
	assert.ErrorIs(t, err, ErrConflict, "over the per-order contract limit")
	_, err = h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1, LimitPrice: c.GetEntryLimit() * 3})
	assert.ErrorIs(t, err, ErrConflict, "a limit far over the ask")

	h.clock.Set(h.frames[0].AsOf.Add(5 * time.Minute))
	_, err = h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "old", "a stale quote is refused")

	h.broker.set(func(b *fakeBroker) { b.quoteTime = h.clock.Now() })
	h.broker.set(func(b *fakeBroker) { b.optionsLevel = "option_level_1" })
	_, err = h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	assert.ErrorIs(t, err, ErrConflict, "the account must be allowed to buy options")

	h.broker.set(func(b *fakeBroker) { b.optionsLevel = "option_level_2" })
	h.broker.set(func(b *fakeBroker) { b.buyingPower = 100 })
	_, err = h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	assert.ErrorIs(t, err, ErrConflict, "buying power must cover the order and the reserve")
}

func TestOnlyAnApprovedCandidateCanBeOrdered(t *testing.T) {
	h := newLabHarness(t)
	h.goLive()
	snap, _ := snapshotOf(t, h.frames[0])
	q := liquidCall(t, snap)
	c := &leapmuxv1.TradingCandidate{Id: "cand-2", RunId: "run-1", Source: leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN, RecommendationId: "r1",
		Status: leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE, OccSymbol: q.OCCSymbol, EntryLimit: q.Ask}
	require.NoError(t, putCandidate(h.ctx, h.db, userID, c, 1))
	_, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	assert.ErrorIs(t, err, ErrConflict, "an AI recommendation alone is never ordered")
}

func TestConfirmNeedsThePersonTheExactOrderAndTime(t *testing.T) {
	h := newLabHarness(t)
	h.goLive()
	c := h.approvedCandidate()
	o, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	require.NoError(t, err)
	confirm := func(fingerprint string, byPerson bool) error {
		_, err := h.lab.Confirm(h.ctx, userID, &leapmuxv1.ConfirmTradingOrderRequest{OrderId: o.GetId(), Fingerprint: fingerprint}, byPerson)
		return err
	}
	assert.ErrorIs(t, confirm(o.GetFingerprint(), false), ErrDenied, "never an agent, a token or an app")
	assert.ErrorIs(t, confirm(strings.Repeat("a", 64), true), ErrDenied, "a different order's fingerprint")
	assert.Zero(t, h.broker.placements())

	h.clock.Set(h.frames[0].AsOf.Add(3 * time.Minute))
	assert.ErrorIs(t, confirm(o.GetFingerprint(), true), ErrConflict, "the window closed")
	got, err := getOrder(h.ctx, h.db, userID, o.GetId())
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_EXPIRED, got.GetState())
	assert.Zero(t, h.broker.placements())
}

func TestConfirmSubmitsOnceAndReconcilesFillsIntoTheLiveBook(t *testing.T) {
	h := newLabHarness(t)
	h.goLive()
	c := h.approvedCandidate()
	o, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 2})
	require.NoError(t, err)
	req := &leapmuxv1.ConfirmTradingOrderRequest{OrderId: o.GetId(), Fingerprint: o.GetFingerprint()}
	placed, err := h.lab.Confirm(h.ctx, userID, req, true)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED, placed.GetState())
	_, err = h.lab.Confirm(h.ctx, userID, req, true)
	assert.ErrorIs(t, err, ErrConflict, "a second confirmation of one order")
	assert.Equal(t, 1, h.broker.placements())
	cand, _ := getCandidate(h.ctx, h.db, userID, c.GetId())
	assert.Equal(t, leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ORDERED, cand.GetStatus())

	h.broker.fill(placed.GetBrokerOrderId(), 1, 1.10)
	_, err = h.lab.Reconcile(h.ctx, userID)
	require.NoError(t, err)
	_, err = h.lab.Reconcile(h.ctx, userID)
	require.NoError(t, err, "reconciling twice records each fill once")
	got, _ := getOrder(h.ctx, h.db, userID, o.GetId())
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PARTIALLY_FILLED, got.GetState())
	assert.Equal(t, uint32(1), got.GetFilledQuantity())
	h.broker.fill(placed.GetBrokerOrderId(), 1, 1.20)
	_, err = h.lab.Reconcile(h.ctx, userID)
	require.NoError(t, err)
	got, _ = getOrder(h.ctx, h.db, userID, o.GetId())
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_FILLED, got.GetState())
	assert.InDelta(t, 1.15, got.GetAverageFillPrice(), 1e-9)

	positions, err := listPositions(h.ctx, h.db, userID)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	live := positions[0]
	assert.Equal(t, leapmuxv1.TradingBook_TRADING_BOOK_LIVE, live.GetBook(), "real fills go to the live book only")
	assert.Equal(t, uint32(2), live.GetQuantity())
	assert.InDelta(t, 1.10*100+1.20*100+0.06, live.GetCost(), 1e-6)
	var fills int
	require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM trading_live_fills`).Scan(&fills))
	assert.Equal(t, 2, fills)
}

func TestALostSubmissionReplyIsReconciledNeverResent(t *testing.T) {
	h := newLabHarness(t)
	h.goLive()
	c := h.approvedCandidate()
	o, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	require.NoError(t, err)
	h.broker.set(func(b *fakeBroker) { b.dropPlaceReply = true })
	got, err := h.lab.Confirm(h.ctx, userID, &leapmuxv1.ConfirmTradingOrderRequest{OrderId: o.GetId(), Fingerprint: o.GetFingerprint()}, true)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTING, got.GetState(), "an unknown outcome is not a failure")
	assert.Equal(t, 1, h.broker.placements())

	_, err = h.lab.Cancel(h.ctx, userID, o.GetId())
	assert.ErrorIs(t, err, ErrConflict, "an order of unknown outcome is reconciled before anything else")

	h.broker.set(func(b *fakeBroker) { b.dropPlaceReply = false })
	_, err = h.lab.Reconcile(h.ctx, userID)
	require.NoError(t, err)
	got, _ = getOrder(h.ctx, h.db, userID, o.GetId())
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED, got.GetState())
	assert.Equal(t, h.broker.orderIDs()[0], got.GetBrokerOrderId(), "found by its reference")
	assert.Equal(t, 1, h.broker.placements(), "reconcile never resends")
}

func TestARejectedSubmissionIsFinal(t *testing.T) {
	h := newLabHarness(t)
	h.goLive()
	c := h.approvedCandidate()
	o, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	require.NoError(t, err)
	h.broker.set(func(b *fakeBroker) { b.rejectPlace = "insufficient options approval" })
	got, err := h.lab.Confirm(h.ctx, userID, &leapmuxv1.ConfirmTradingOrderRequest{OrderId: o.GetId(), Fingerprint: o.GetFingerprint()}, true)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_REJECTED, got.GetState())
	assert.Contains(t, got.GetDetail(), "insufficient options approval")
	cand, _ := getCandidate(h.ctx, h.db, userID, c.GetId())
	assert.Equal(t, leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_APPROVED, cand.GetStatus(), "it can be prepared again")
}

func TestAClosingOrderNeedsAConfirmationOrAScopedPolicy(t *testing.T) {
	h := newLabHarness(t)
	h.goLive()
	c := h.approvedCandidate()
	o, err := h.lab.Prepare(h.ctx, userID, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: c.GetId(), Quantity: 1})
	require.NoError(t, err)
	placed, err := h.lab.Confirm(h.ctx, userID, &leapmuxv1.ConfirmTradingOrderRequest{OrderId: o.GetId(), Fingerprint: o.GetFingerprint()}, true)
	require.NoError(t, err)
	h.broker.fill(placed.GetBrokerOrderId(), 1, c.GetEntryLimit())
	_, err = h.lab.Reconcile(h.ctx, userID)
	require.NoError(t, err)
	positions, _ := listPositions(h.ctx, h.db, userID)
	live := positions[0]

	// Marking does nothing to a live position without a policy.
	_, err = h.lab.MarkBooks(h.ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, 1, h.broker.placements())

	policy := &leapmuxv1.TradingExecutionPolicy{
		PositionId: live.GetId(), Side: leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_SELL_TO_CLOSE, MaxQuantity: 1,
		LimitPriceBound: 0.01, ExpiresAtMs: h.frames[0].AsOf.Add(24 * time.Hour).UnixMilli(),
	}
	_, err = h.lab.SavePolicy(h.ctx, userID, &leapmuxv1.SaveTradingExecutionPolicyRequest{Policy: policy}, false)
	assert.ErrorIs(t, err, ErrDenied, "an agent cannot approve a policy")
	opening := proto.Clone(policy).(*leapmuxv1.TradingExecutionPolicy)
	opening.Side = leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_BUY_TO_OPEN
	_, err = h.lab.SavePolicy(h.ctx, userID, &leapmuxv1.SaveTradingExecutionPolicyRequest{Policy: opening}, true)
	assert.ErrorIs(t, err, ErrInvalid, "a policy never opens a position")
	wide := proto.Clone(policy).(*leapmuxv1.TradingExecutionPolicy)
	wide.MaxQuantity = 5
	_, err = h.lab.SavePolicy(h.ctx, userID, &leapmuxv1.SaveTradingExecutionPolicyRequest{Policy: wide}, true)
	assert.ErrorIs(t, err, ErrInvalid, "a policy cannot cover more than is held")
	saved, err := h.lab.SavePolicy(h.ctx, userID, &leapmuxv1.SaveTradingExecutionPolicyRequest{Policy: policy}, true)
	require.NoError(t, err)

	_, err = h.lab.MarkBooks(h.ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, 2, h.broker.placements(), "the policy closed the position once")
	_, err = h.lab.MarkBooks(h.ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, 2, h.broker.placements(), "a policy is used once")
	p, _ := getPolicy(h.ctx, h.db, userID, saved.GetId())
	assert.False(t, p.GetActive())
	orders, _ := listOrders(h.ctx, h.db, userID)
	var sale *leapmuxv1.TradingOrder
	for _, o := range orders {
		if o.GetSide() == leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_SELL_TO_CLOSE {
			sale = o
		}
	}
	require.NotNil(t, sale)
	assert.Equal(t, saved.GetId(), sale.GetPolicyId())
	assert.Equal(t, "policy "+saved.GetId(), sale.GetConfirmedBy())
}

func TestSavingSettingsClearsAnOldConfirmationPassphrase(t *testing.T) {
	h := newLabHarness(t)
	_, err := h.db.Exec(`UPDATE trading_lab_settings SET passphrase_hash = 'argon2id$old'`)
	require.NoError(t, err)
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	_, err = h.lab.SaveSettings(h.ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	require.NoError(t, err)
	var stored string
	require.NoError(t, h.db.QueryRow(`SELECT passphrase_hash FROM trading_lab_settings`).Scan(&stored))
	assert.Empty(t, stored, "approval in the browser replaced it")
}
