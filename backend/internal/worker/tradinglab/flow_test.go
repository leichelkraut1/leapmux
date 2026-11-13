package tradinglab_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/tradinglab"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// agents is a one-computer platform: every agent the engine opens is
// recorded with its request, and every message it is sent.
type agents struct {
	mu       sync.Mutex
	opened   []*leapmuxv1.OpenAgentRequest
	ids      []string
	messages map[string][]string
}

func (a *agents) Workers(context.Context) ([]workflow.WorkerCandidate, error) {
	return []workflow.WorkerCandidate{{ID: "w1", Name: "Lab PC", Online: true}}, nil
}

func (a *agents) OpenAgent(_ context.Context, _ string, req *leapmuxv1.OpenAgentRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := fmt.Sprintf("agent-%d", len(a.opened)+1)
	a.opened, a.ids = append(a.opened, req), append(a.ids, id)
	return id, nil
}

func (a *agents) AddAgentTab(context.Context, string, string, string) error { return nil }

func (a *agents) EnqueueInput(_ context.Context, _, agentID, _, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages[agentID] = append(a.messages[agentID], text)
	return nil
}

func (a *agents) AgentStates(_ context.Context, _ string, ids []string) (map[string]workflow.AgentState, error) {
	out := map[string]workflow.AgentState{}
	for _, id := range ids {
		out[id] = workflow.AgentAlive
	}
	return out, nil
}

func (a *agents) InterruptAgent(context.Context, string, string) error { return nil }

func (a *agents) FindAgents(_ context.Context, _, runID string) (map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]string{}
	for i, req := range a.opened {
		if req.GetWorkflow().GetRunId() == runID {
			out[req.GetWorkflow().GetSlotId()] = a.ids[i]
		}
	}
	return out, nil
}

// agent finds the agent opened for a role, its credential and its messages.
func (a *agents) agent(t *testing.T, runID, role string) (*leapmuxv1.OpenAgentRequest, *leapmuxv1.WorkflowAgentAuth, string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, req := range a.opened {
		if req.GetWorkflow().GetRoleKey() == role {
			return req, &leapmuxv1.WorkflowAgentAuth{RunId: runID, Token: req.GetWorkflow().GetToken()}, strings.Join(a.messages[a.ids[i]], "\n")
		}
	}
	t.Fatalf("no agent for %s", role)
	return nil, nil, ""
}

const user = "user-1"

// recommendation builds a consistent long-option idea from a snapshot quote.
func recommendation(snap *tradinglab.Snapshot, q tradinglab.OptionQuote, fee float64) map[string]any {
	limit := math.Round((q.Ask+0.02)*100) / 100
	strategy, direction := "long_call", "bullish"
	if q.Right == "put" {
		strategy, direction = "long_put", "bearish"
	}
	return map[string]any{
		"id": "r1", "underlying": q.Underlying, "direction": direction, "strategy": strategy,
		"contract": map[string]any{"occ_symbol": q.OCCSymbol, "expiration": q.Expiration, "strike": q.Strike, "right": q.Right},
		"entry":    map[string]any{"limit_price": limit},
		"thesis":   "The scheduled earnings report should move the stock more than the option prices in.",
		"catalyst": map[string]any{"type": "earnings", "description": "Quarterly earnings report"},
		"evidence": []any{map[string]any{"claim": "Implied volatility is modest into the event", "source": "options[0]"}},
		"scenarios": []any{
			map[string]any{"name": "move", "probability": 0.35, "underlying_price": q.Strike * 1.05, "option_value": limit * 3},
			map[string]any{"name": "no move", "probability": 0.65, "underlying_price": q.Strike, "option_value": 0.1},
		},
		"probabilities": map[string]any{"profit": 0.3, "reach_3x": 0.12, "reach_5x": 0.05},
		"risk":          map[string]any{"max_loss_per_contract": math.Round((limit*100+fee)*100) / 100},
		"exits": map[string]any{
			"take_profit": []any{map[string]any{"price": limit * 3, "fraction": 0.5}},
			"stop_loss":   limit / 2, "time_stop": snap.AsOf.AddDate(0, 0, 2).Format(time.DateOnly),
		},
		"invalidation": []any{"The stock closes through the strike the wrong way"},
		"horizon_days": 2,
	}
}

func liquid(snap *tradinglab.Snapshot, sym, right string) tradinglab.OptionQuote {
	under := snap.Underlying(sym).Last
	var best *tradinglab.OptionQuote
	for i := range snap.Options {
		o := &snap.Options[i]
		if o.Underlying != sym || o.Right != right || o.OpenInterest < 100 || o.Volume < 10 || (o.Ask-o.Bid)/o.Ask > 0.25 {
			continue
		}
		if best == nil || math.Abs(o.Strike-under) < math.Abs(best.Strike-under) {
			best = o
		}
	}
	return *best
}

func report(t *testing.T, e *workflow.Engine, auth *leapmuxv1.WorkflowAgentAuth, doc any) error {
	t.Helper()
	ctx, err := e.AgentContext(t.Context(), user, auth)
	if err != nil {
		return err // a retired session is refused before it can report
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	var taskID string
	for _, task := range ctx.GetTasks() {
		if task.GetStatus() == leapmuxv1.WorkflowTaskStatus_WORKFLOW_TASK_STATUS_RUNNING {
			taskID = task.GetId()
		}
	}
	_, err = e.Report(t.Context(), user, &leapmuxv1.WorkflowAgentReportRequest{
		Auth: auth, TaskId: taskID,
		Result: &leapmuxv1.WorkflowTaskResult{Outcome: leapmuxv1.WorkflowTaskOutcome_WORKFLOW_TASK_OUTCOME_COMPLETED, Summary: "Submitted.", StructuredOutput: string(raw)},
	})
	return err
}

// TestBlindTradingLabRunsEndToEndInPaperMode drives the bundled template
// through the workflow engine with no credentials: the snapshot is frozen
// from a replay, both analysts work at once in isolation, the evaluation and
// the audit read anonymized submissions, the record books the shadow books,
// and no order can be placed.
func TestBlindTradingLabRunsEndToEndInPaperMode(t *testing.T) {
	ctx := t.Context()
	db, err := workerdb.Open(filepath.Join(t.TempDir(), "worker.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, workerdb.Migrate(ctx, db))
	replay := t.TempDir()
	_, err = tradinglab.GenerateSampleReplay(replay, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), 8, 3)
	require.NoError(t, err)
	clock := testutil.NewQuartzMock(t)
	clock.Set(time.Date(2026, 11, 2, 20, 31, 0, 0, time.UTC))

	platform := &agents{messages: map[string][]string{}}
	newEngine := func() *workflow.Engine {
		e := workflow.New(workflow.Config{DB: db, WorkerID: "w1", Clock: clock})
		lab := tradinglab.New(tradinglab.Config{DB: db, Clock: clock, NewID: func() string { return fmt.Sprintf("id-%d", time.Now().UnixNano()) }})
		lab.Register(e)
		e.SetPlatform(platform)
		return e
	}
	engine := newEngine()
	lab := tradinglab.New(tradinglab.Config{DB: db, Clock: clock, NewID: func() string { return fmt.Sprintf("lab-%d", time.Now().UnixNano()) }})
	settings := tradinglab.DefaultSettings()
	settings.ReplayDir, settings.Watchlist = replay, tradinglab.SampleSymbols
	_, err = lab.SaveSettings(ctx, user, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: settings})
	require.NoError(t, err)

	bundle, err := tradinglab.TemplateBundle()
	require.NoError(t, err)
	created, err := engine.CreateRun(ctx, user, &leapmuxv1.CreateWorkflowRunRequest{
		Template: bundle.GetTemplate(), Profiles: bundle.GetProfiles(), Objective: "Daily scan", WorkingDir: "/lab", WorkspaceId: "ws",
	})
	require.NoError(t, err)
	runID := created.GetRun().GetId()
	step := func(e *workflow.Engine) {
		e.Pass(ctx, true)
		e.WaitExecutors()
		e.Pass(ctx, true)
	}
	step(engine)

	// Both analysts start together, each confined to its run in a private
	// directory, and neither is told the other exists.
	blindReq, blindAuth, blindText := platform.agent(t, runID, "blind-analyst")
	contraReq, contraAuth, contraText := platform.agent(t, runID, "contrarian-analyst")
	for _, req := range []*leapmuxv1.OpenAgentRequest{blindReq, contraReq} {
		assert.True(t, req.GetWorkflow().GetConfine())
		assert.True(t, req.GetWorkflow().GetPrivateDir())
	}
	assert.Contains(t, contraText, "crowded positioning")
	assert.NotContains(t, blindText, "crowded positioning", "the blind analyst never sees the contrarian hypothesis")
	assert.NotContains(t, blindText, "Daily scan")

	// A restart between dispatch and the reports changes nothing.
	engine = newEngine()
	engine.Pass(ctx, true)

	actx, err := engine.AgentContext(ctx, user, blindAuth)
	require.NoError(t, err)
	require.Len(t, actx.GetInputs(), 1)
	snap, err := tradinglab.DecodeSnapshot(actx.GetInputs()[0].GetJson())
	require.NoError(t, err)
	snapSHA := actx.GetInputs()[0].GetSha256()
	assert.Contains(t, blindText, snapSHA, "the analyst is told the hash it must cite")

	fee := settings.GetSimulation().GetFeePerContract()
	blindPick := map[string]any{"schema_version": 1, "snapshot_sha256": snapSHA, "no_trade": false,
		"recommendations": []any{recommendation(snap, liquid(snap, "SYNA", "call"), fee)}}
	contraPick := map[string]any{"schema_version": 1, "snapshot_sha256": snapSHA, "no_trade": false,
		"recommendations": []any{recommendation(snap, liquid(snap, "SYNB", "put"), fee)}}
	require.Error(t, report(t, engine, blindAuth, map[string]any{"schema_version": 1}), "the schema refuses an incomplete prediction")
	require.NoError(t, report(t, engine, blindAuth, blindPick))
	require.NoError(t, report(t, engine, contraAuth, contraPick))
	assert.ErrorIs(t, report(t, engine, blindAuth, blindPick), workflow.ErrDenied, "a frozen prediction cannot be resubmitted")
	step(engine)

	_, evalAuth, evalText := platform.agent(t, runID, "evaluator")
	_, auditAuth, auditText := platform.agent(t, runID, "risk-auditor")
	for _, text := range []string{evalText, auditText} {
		assert.Contains(t, text, "### Submission A")
		assert.Contains(t, text, "### Submission B")
		assert.NotContains(t, text, "crowded positioning")
		assert.NotContains(t, text, "contrarian")
	}
	scores := map[string]int{"evidence": 3, "pricing": 3, "catalyst": 4, "risk_reward": 3, "calibration": 3, "executability": 4}
	var subs, findings []any
	for _, label := range []string{"Submission A", "Submission B"} {
		subs = append(subs, map[string]any{"label": label, "recommendations": []any{
			map[string]any{"recommendation_id": "r1", "scores": scores, "verdict": "acceptable", "notes": "Same rubric."},
		}})
		findings = append(findings, map[string]any{"label": label, "recommendation_id": "r1", "verdict": "pass", "issues": []any{},
			"computed_max_loss_per_contract": 100.0,
			"checks": map[string]any{"evidence_supported": true, "price_consistent_with_quote": true, "contract_exists": true,
				"liquidity_adequate": true, "max_loss_correct": true, "catalyst_within_horizon": true}})
	}
	require.NoError(t, report(t, engine, evalAuth, map[string]any{"schema_version": 1, "rubric_version": "options-v1", "submissions": subs}))
	require.NoError(t, report(t, engine, auditAuth, map[string]any{"schema_version": 1, "findings": findings}))
	step(engine)
	step(engine)

	detail, err := engine.GetRun(ctx, user, runID)
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.WorkflowRunStatus_WORKFLOW_RUN_STATUS_COMPLETED, detail.GetRun().GetStatus(), detail.GetRun().GetStatusDetail())
	assert.Len(t, detail.GetFrozenOutputs(), 6, "snapshot, two predictions, evaluation, audit and record")
	bad, err := engine.VerifyFrozen(ctx, runID)
	require.NoError(t, err)
	assert.Empty(t, bad)

	view, err := lab.RunView(ctx, user, runID)
	require.NoError(t, err)
	assert.Equal(t, snapSHA, view.GetSnapshot().GetSha256())
	require.Len(t, view.GetCandidates(), 2)
	for _, c := range view.GetCandidates() {
		assert.Equal(t, leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE, c.GetStatus(), c.GetRejectionReasons())
		assert.InDelta(t, 66, c.GetEvaluationScore(), 1e-9, "both scored by the one rubric")
		assert.Equal(t, "pass", c.GetAuditVerdict())
	}
	sources := slices.Sorted(maps.Values(view.GetLabelSources()))
	assert.Equal(t, []string{"BLIND", "CONTRARIAN"}, sources)

	dash, err := lab.Dashboard(ctx, user)
	require.NoError(t, err)
	books := map[leapmuxv1.TradingBook]int{}
	for _, p := range dash.GetPositions() {
		books[p.GetBook()]++
		assert.NotEqual(t, leapmuxv1.TradingBook_TRADING_BOOK_LIVE, p.GetBook(), "paper mode has no live fills")
	}
	assert.Equal(t, map[leapmuxv1.TradingBook]int{
		leapmuxv1.TradingBook_TRADING_BOOK_BLIND: 1, leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN: 1, leapmuxv1.TradingBook_TRADING_BOOK_BLIND_INVERSE: 1,
	}, books)

	approved, err := lab.DecideCandidate(ctx, user, &leapmuxv1.DecideTradingCandidateRequest{CandidateId: view.GetCandidates()[0].GetId(), Approve: true})
	require.NoError(t, err)
	_, err = lab.Prepare(ctx, user, &leapmuxv1.PrepareTradingOrderRequest{CandidateId: approved.GetId(), Quantity: 1})
	assert.Error(t, err, "paper mode never reaches a broker")

	marked, err := lab.MarkBooks(ctx, user)
	require.NoError(t, err)
	assert.Positive(t, marked.GetMarked())
	dash, err = lab.Dashboard(ctx, user)
	require.NoError(t, err)
	for _, m := range dash.GetMetrics() {
		if m.GetBook() == leapmuxv1.TradingBook_TRADING_BOOK_LIVE {
			assert.Zero(t, m.GetIdeas())
			continue
		}
		assert.Equal(t, uint32(1), m.GetIdeas(), m.GetBook().String())
		assert.NotEmpty(t, m.GetPnlAtHorizon(), "every book is marked at the same horizons")
	}
}
