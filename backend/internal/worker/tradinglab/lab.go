package tradinglab

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// Errors the service maps onto RPC codes.
var (
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("not allowed in the current state")
	ErrDenied   = errors.New("not permitted")
)

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func conflictf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

func deniedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDenied, fmt.Sprintf(format, args...))
}

// Executor names the template refers to.
const (
	ExecutorSnapshot = "tradinglab.snapshot"
	ExecutorRecord   = "tradinglab.record"
)

// BrokerSession is a connected broker: both views and its verification.
// *Robinhood is the production one.
type BrokerSession interface {
	TradingBroker
	Reader() ReadOnlyBroker
	Status(ctx context.Context, now time.Time) *leapmuxv1.TradingBrokerStatus
	Close() error
}

// Config configures a Lab.
type Config struct {
	DB     *sql.DB
	Clock  quartz.Clock
	Logger *slog.Logger
	NewID  func() string
	// Connect opens a broker session from the settings. Nil connects to the
	// configured Robinhood MCP server.
	Connect func(ctx context.Context, cfg *leapmuxv1.TradingMcpConfig) (BrokerSession, error)
	// ConnectorURL is the read-only Claude connector's public URL when this
	// worker serves it, shown in the settings.
	ConnectorURL string
}

// Lab is the trading lab of one coordinator.
type Lab struct {
	store *store
	clock quartz.Clock
	log   *slog.Logger
	newID func() string
	// connect opens the broker for a user, whose sign-in it may need.
	connect func(ctx context.Context, userID string, cfg *leapmuxv1.TradingMcpConfig) (BrokerSession, error)
	// signIns holds the broker sign-ins waiting for the browser to return.
	signIns brokerSignIns
	// connectorURL is Config.ConnectorURL.
	connectorURL string
	// gateMu serializes the gateway's state transitions, so a double click
	// cannot interleave two confirmations of one order.
	gateMu sync.Mutex
}

// New builds a Lab.
func New(cfg Config) *Lab {
	clock := cfg.Clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	l := &Lab{store: &store{db: cfg.DB}, clock: clock, log: log.With("component", "tradinglab"), newID: cfg.NewID, connectorURL: cfg.ConnectorURL}
	if cfg.Connect != nil {
		l.connect = func(ctx context.Context, _ string, c *leapmuxv1.TradingMcpConfig) (BrokerSession, error) {
			return cfg.Connect(ctx, c)
		}
	} else {
		l.connect = func(ctx context.Context, userID string, c *leapmuxv1.TradingMcpConfig) (BrokerSession, error) {
			return ConnectRobinhood(ctx, c, nil, func(ctx context.Context) (string, error) { return l.brokerToken(ctx, userID, c.GetUrl()) })
		}
	}
	return l
}

func (l *Lab) now() time.Time { return l.clock.Now().UTC() }

// Register adds the lab's executors to a workflow engine.
func (l *Lab) Register(e *workflow.Engine) {
	e.RegisterExecutor(ExecutorSnapshot, workflow.ExecutorFunc(l.snapshotExecutor))
	e.RegisterExecutor(ExecutorRecord, workflow.ExecutorFunc(l.recordExecutor))
}

// --- Settings ---

// DefaultSettings is a paper lab on recorded data: nothing it does can reach
// a broker.
func DefaultSettings() *leapmuxv1.TradingLabSettings {
	return &leapmuxv1.TradingLabSettings{
		Mode:       leapmuxv1.TradingLabMode_TRADING_LAB_MODE_PAPER,
		MarketData: leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_REPLAY,
		Robinhood:  &leapmuxv1.TradingMcpConfig{Transport: leapmuxv1.TradingMcpTransport_TRADING_MCP_TRANSPORT_HTTP},
		Risk: &leapmuxv1.TradingRiskBudget{
			MaxLossPerOrder: 250, MaxDailyRisk: 500, MaxOpenRisk: 1000, MaxContractsPerOrder: 5, MaxOpenPositions: 5,
			MinBuyingPowerReserve: 500, ConfirmationTtlSeconds: 120, MaxQuoteAgeSeconds: 30,
		},
		Simulation: &leapmuxv1.TradingSimulationRules{
			StartingCapital: 10000, AllocationPerIdea: 500, FeePerContract: 0.03, SlippagePerContract: 2,
			MaxSpreadFraction: 0.25, MinOpenInterest: 100, MinVolume: 10, HorizonDays: []uint32{1, 3, 5},
		},
		Watchlist:             []string{"SPY", "QQQ"},
		MaxSnapshotAgeSeconds: 900,
	}
}

func normalizeSettings(s *leapmuxv1.TradingLabSettings) error {
	d := DefaultSettings()
	if s.GetMode() == leapmuxv1.TradingLabMode_TRADING_LAB_MODE_UNSPECIFIED {
		s.Mode = d.GetMode()
	}
	if s.GetMarketData() == leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_UNSPECIFIED {
		s.MarketData = d.GetMarketData()
	}
	if s.GetRobinhood() == nil {
		s.Robinhood = d.GetRobinhood()
	}
	if s.GetRisk() == nil {
		s.Risk = d.GetRisk()
	}
	if s.GetSimulation() == nil {
		s.Simulation = d.GetSimulation()
	}
	if s.GetMaxSnapshotAgeSeconds() == 0 {
		s.MaxSnapshotAgeSeconds = d.GetMaxSnapshotAgeSeconds()
	}
	var watch []string
	for _, w := range s.GetWatchlist() {
		w = strings.ToUpper(strings.TrimSpace(w))
		if w != "" && !slices.Contains(watch, w) {
			watch = append(watch, w)
		}
	}
	s.Watchlist = watch
	sim := s.GetSimulation()
	if sim.GetStartingCapital() <= 0 || sim.GetAllocationPerIdea() <= 0 || sim.GetAllocationPerIdea() > sim.GetStartingCapital() {
		return invalidf("the allocation per idea must be positive and within the starting capital")
	}
	if sim.GetFeePerContract() < 0 || sim.GetSlippagePerContract() < 0 || sim.GetMaxSpreadFraction() < 0 {
		return invalidf("fees, slippage and the spread limit cannot be negative")
	}
	if len(sim.GetHorizonDays()) == 0 {
		sim.HorizonDays = d.GetSimulation().GetHorizonDays()
	}
	slices.Sort(sim.HorizonDays)
	sim.HorizonDays = slices.Compact(sim.HorizonDays)
	r := s.GetRisk()
	if r.GetMaxLossPerOrder() <= 0 || r.GetMaxDailyRisk() <= 0 || r.GetMaxOpenRisk() <= 0 || r.GetMaxContractsPerOrder() == 0 || r.GetMaxOpenPositions() == 0 {
		return invalidf("every risk limit must be set and positive")
	}
	if r.GetConfirmationTtlSeconds() == 0 || r.GetConfirmationTtlSeconds() > 900 {
		return invalidf("a confirmation must expire within 1 to 900 seconds")
	}
	if r.GetMaxQuoteAgeSeconds() == 0 {
		r.MaxQuoteAgeSeconds = d.GetRisk().GetMaxQuoteAgeSeconds()
	}
	if c := s.GetRobinhood(); c.GetTokenEnv() != "" && c.GetTokenFile() != "" {
		return invalidf("the token comes from an environment variable or a file, not both")
	}
	return nil
}

// Settings returns the user's settings.
func (l *Lab) Settings(ctx context.Context, userID string) (*leapmuxv1.TradingLabSettings, error) {
	s, err := getSettings(ctx, l.store.db, userID)
	if err != nil {
		return nil, err
	}
	return s, l.fillConnector(ctx, l.store.db, userID, s)
}

// fillConnector sets the settings' derived fields, which are never stored
// with them: the connector's and the broker sign-in's.
func (l *Lab) fillConnector(ctx context.Context, q querier, userID string, s *leapmuxv1.TradingLabSettings) error {
	hash, err := getConnectorHash(ctx, q, userID)
	if err != nil {
		return err
	}
	s.HasConnectorPassphrase, s.ConnectorUrl = hash != "", l.connectorURL
	s.BrokerSignIn, err = brokerSignInStatus(ctx, q, userID)
	return err
}

// SaveSettings validates and stores settings, and sets the connector
// passphrase. Switching to LIVE needs a broker verified as live-ready; the
// service lets only the person's browser session ask for it.
func (l *Lab) SaveSettings(ctx context.Context, userID string, req *leapmuxv1.SaveTradingLabSettingsRequest) (*leapmuxv1.TradingLabSettings, error) {
	s := proto.Clone(req.GetSettings()).(*leapmuxv1.TradingLabSettings)
	if s == nil {
		return nil, invalidf("missing settings")
	}
	if err := normalizeSettings(s); err != nil {
		return nil, err
	}
	var out *leapmuxv1.TradingLabSettings
	err := l.store.inTx(ctx, func(q querier) error {
		if s.GetMode() == leapmuxv1.TradingLabMode_TRADING_LAB_MODE_LIVE {
			check, err := getBrokerCheck(ctx, q, userID)
			if err != nil {
				return err
			}
			if !check.GetLiveReady() {
				return conflictf("live mode needs a broker verified as ready for live trading; verify the broker first")
			}
		}
		s.UpdatedAtMs = l.now().UnixMilli()
		s.HasConnectorPassphrase, s.ConnectorUrl, s.BrokerSignIn = false, "", nil
		if err := putSettings(ctx, q, userID, s); err != nil {
			return err
		}
		if pass := req.GetNewConnectorPassphrase(); pass != "" {
			if err := setConnectorPassphrase(ctx, q, userID, pass, s.GetUpdatedAtMs()); err != nil {
				return err
			}
		} else if req.GetSignOutConnector() {
			if err := signOutConnector(ctx, q, userID); err != nil {
				return err
			}
		}
		out = s
		return l.fillConnector(ctx, q, userID, s)
	})
	return out, err
}

// VerifyBroker connects to the configured server and records what it offers.
func (l *Lab) VerifyBroker(ctx context.Context, userID string) (*leapmuxv1.TradingBrokerStatus, error) {
	s, err := l.Settings(ctx, userID)
	if err != nil {
		return nil, err
	}
	var st *leapmuxv1.TradingBrokerStatus
	sess, err := l.connect(ctx, userID, s.GetRobinhood())
	if err != nil {
		st = &leapmuxv1.TradingBrokerStatus{
			Configured: s.GetRobinhood().GetUrl() != "" || s.GetRobinhood().GetCommand() != "",
			Detail:     err.Error(), CheckedAtMs: l.now().UnixMilli(),
		}
	} else {
		defer func() { _ = sess.Close() }()
		st = sess.Status(ctx, l.now())
	}
	if err := putBrokerCheck(ctx, l.store.db, userID, st); err != nil {
		return nil, err
	}
	return st, nil
}

// marketData opens the configured market data source. The returned closer
// ends any broker session it opened.
func (l *Lab) marketData(ctx context.Context, userID string, s *leapmuxv1.TradingLabSettings) (MarketData, func(), error) {
	switch s.GetMarketData() {
	case leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_ROBINHOOD:
		sess, err := l.connect(ctx, userID, s.GetRobinhood())
		if err != nil {
			return nil, nil, err
		}
		return &LiveMarket{Broker: sess.Reader(), Now: l.now}, func() { _ = sess.Close() }, nil
	default:
		return &Replay{Dir: s.GetReplayDir()}, func() {}, nil
	}
}

// --- Executors ---

func param(step *leapmuxv1.WorkflowStep, key, fallback string) string {
	if v := strings.TrimSpace(step.GetParams()[key]); v != "" {
		return v
	}
	return fallback
}

func intParam(step *leapmuxv1.WorkflowStep, key string, fallback int) int {
	if n, err := strconv.Atoi(param(step, key, "")); err == nil && n > 0 {
		return n
	}
	return fallback
}

// snapshotExecutor captures, checks and stores the run's market snapshot.
// A re-run for the same task returns the snapshot already stored, so both
// analysts can only ever see one market.
func (l *Lab) snapshotExecutor(ctx context.Context, req workflow.ExecutorRequest) (*workflow.ExecutorResult, error) {
	if existing, err := snapshotForTask(ctx, l.store.db, req.Task.GetId()); err == nil {
		return &workflow.ExecutorResult{Summary: "Reused the snapshot captured for this task (sha256 " + short(existing.sha256) + ")", StructuredOutput: existing.canonical}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	s, err := l.Settings(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	var symbols []string
	if raw := param(req.Step, "symbols", ""); raw != "" {
		symbols = strings.Split(raw, ",")
	} else {
		symbols = s.GetWatchlist()
	}
	if len(symbols) == 0 {
		return nil, &workflow.PermanentError{Err: errors.New("no underlyings: set the lab's watchlist or the step's symbols")}
	}
	md, closeMD, err := l.marketData(ctx, req.UserID, s)
	if err != nil {
		return nil, &workflow.PermanentError{Err: fmt.Errorf("market data: %w", err)}
	}
	defer closeMD()
	maxDTE := intParam(req.Step, "max_days_to_expiry", 14)
	frame, prov, now, err := md.Capture(ctx, symbols, maxDTE)
	if err != nil {
		return nil, err
	}
	source := "replay"
	if md.Source() == leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_ROBINHOOD {
		source = "robinhood"
	}
	snap, err := BuildSnapshot(frame, source, prov, SnapshotOptions{
		Symbols: symbols, MaxDTE: maxDTE, StrikesPerSide: intParam(req.Step, "strikes_per_side", 6),
		MaxQuoteAge:    time.Duration(intParam(req.Step, "max_quote_age_seconds", 900)) * time.Second,
		MaxSnapshotAge: time.Duration(s.GetMaxSnapshotAgeSeconds()) * time.Second, Now: now,
	})
	if errors.Is(err, ErrStale) {
		return nil, &workflow.PermanentError{Err: err}
	}
	if err != nil {
		return nil, err
	}
	canonical, sum, err := snap.Canonical()
	if err != nil {
		return nil, err
	}
	stored := &storedSnapshot{
		id: l.newID(), runID: req.Run.GetId(), taskID: req.Task.GetId(), sha256: sum, canonical: canonical,
		asOfMs: snap.AsOf.UnixMilli(), capturedAtMs: l.now().UnixMilli(), source: md.Source(),
	}
	if err := insertSnapshot(ctx, l.store.db, req.UserID, stored); err != nil {
		return nil, err
	}
	return &workflow.ExecutorResult{
		Summary: fmt.Sprintf("Captured %d contracts on %s as of %s (sha256 %s)", len(snap.Options), strings.Join(snap.Rules.Symbols, ", "),
			snap.AsOf.Format(time.RFC3339), short(sum)),
		StructuredOutput: canonical,
		Evidence:         []*leapmuxv1.WorkflowEvidence{{Label: "snapshot", Value: sum}},
	}, nil
}

// The template's step keys, overridable per record step.
const (
	defaultSnapshotStep   = "snapshot"
	defaultBlindStep      = "blind-research"
	defaultContrarianStep = "contrarian-research"
	defaultEvaluationStep = "evaluate"
	defaultAuditStep      = "risk-audit"
)

func frozenByStep(frozen []*leapmuxv1.WorkflowFrozenOutput) map[string]*leapmuxv1.WorkflowFrozenOutput {
	out := map[string]*leapmuxv1.WorkflowFrozenOutput{}
	for _, f := range frozen {
		out[f.GetStepKey()] = f
	}
	return out
}

func candidateID(runID string, book leapmuxv1.TradingBook, recID string) string {
	return "cand-" + workflow.HashHex(runID + "/" + book.String() + "/" + recID)[:24]
}

func positionID(candID string, book leapmuxv1.TradingBook) string {
	return "pos-" + workflow.HashHex(candID + "/" + book.String())[:24]
}

// recordExecutor books a finished run: every recommendation and no-trade as
// a candidate with the lab's checks, the evaluation and the audit, and the
// three shadow books. It runs once per run; a re-run returns the record.
func (l *Lab) recordExecutor(ctx context.Context, req workflow.ExecutorRequest) (*workflow.ExecutorResult, error) {
	runID := req.Run.GetId()
	if v, err := getRunView(ctx, l.store.db, req.UserID, runID); err == nil {
		return recordResult(v), nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	steps := frozenByStep(req.Frozen)
	need := func(key string) (*leapmuxv1.WorkflowFrozenOutput, error) {
		f := steps[param(req.Step, key+"_step", map[string]string{
			"snapshot": defaultSnapshotStep, "blind": defaultBlindStep, "contrarian": defaultContrarianStep,
			"evaluation": defaultEvaluationStep, "audit": defaultAuditStep,
		}[key])]
		if f == nil {
			return nil, &workflow.PermanentError{Err: fmt.Errorf("the run has no frozen %s output", key)}
		}
		return f, nil
	}
	snapF, err := need("snapshot")
	if err != nil {
		return nil, err
	}
	blindF, err := need("blind")
	if err != nil {
		return nil, err
	}
	contraF, err := need("contrarian")
	if err != nil {
		return nil, err
	}
	evalF, err := need("evaluation")
	if err != nil {
		return nil, err
	}
	auditF, err := need("audit")
	if err != nil {
		return nil, err
	}
	snap, err := DecodeSnapshot(snapF.GetCanonicalJson())
	if err != nil {
		return nil, &workflow.PermanentError{Err: err}
	}
	stored, err := snapshotForRun(ctx, l.store.db, req.UserID, runID)
	if err != nil {
		return nil, &workflow.PermanentError{Err: fmt.Errorf("the run's snapshot is not in the lab: %w", err)}
	}
	if stored.sha256 != snapF.GetSha256() {
		return nil, &workflow.PermanentError{Err: errors.New("the frozen snapshot differs from the one the lab captured")}
	}
	preds := map[leapmuxv1.TradingBook]*Prediction{}
	shas := map[leapmuxv1.TradingBook]string{}
	for book, f := range map[leapmuxv1.TradingBook]*leapmuxv1.WorkflowFrozenOutput{
		leapmuxv1.TradingBook_TRADING_BOOK_BLIND: blindF, leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN: contraF,
	} {
		p, err := DecodePrediction(f.GetCanonicalJson())
		if err != nil {
			return nil, &workflow.PermanentError{Err: err}
		}
		preds[book], shas[book] = p, f.GetSha256()
	}
	// The labels the evaluator and the auditor saw, read back to their books.
	labelBook := map[string]leapmuxv1.TradingBook{}
	labelSources := map[string]string{}
	for _, lb := range req.Labels {
		if lb.GetStepKey() != evalF.GetStepKey() {
			continue
		}
		switch lb.GetSourceTaskId() {
		case blindF.GetTaskId():
			labelBook[lb.GetLabel()] = leapmuxv1.TradingBook_TRADING_BOOK_BLIND
		case contraF.GetTaskId():
			labelBook[lb.GetLabel()] = leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN
		}
		labelSources[lb.GetLabel()] = strings.TrimPrefix(labelBook[lb.GetLabel()].String(), "TRADING_BOOK_")
	}
	auditLabelBook := map[string]leapmuxv1.TradingBook{}
	for _, lb := range req.Labels {
		if lb.GetStepKey() != auditF.GetStepKey() {
			continue
		}
		switch lb.GetSourceTaskId() {
		case blindF.GetTaskId():
			auditLabelBook[lb.GetLabel()] = leapmuxv1.TradingBook_TRADING_BOOK_BLIND
		case contraF.GetTaskId():
			auditLabelBook[lb.GetLabel()] = leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN
		}
	}
	bookLabel := map[leapmuxv1.TradingBook]string{}
	for label, book := range labelBook {
		bookLabel[book] = label
	}

	var eval Evaluation
	if err := json.Unmarshal([]byte(evalF.GetCanonicalJson()), &eval); err != nil {
		return nil, &workflow.PermanentError{Err: fmt.Errorf("decode the evaluation: %w", err)}
	}
	submissions := map[string]*Prediction{}
	for label, book := range labelBook {
		submissions[label] = preds[book]
	}
	scores, verdicts, evalProblems := EvaluationScores(&eval, submissions)
	var audit Audit
	if err := json.Unmarshal([]byte(auditF.GetCanonicalJson()), &audit); err != nil {
		return nil, &workflow.PermanentError{Err: fmt.Errorf("decode the audit: %w", err)}
	}
	type finding struct {
		verdict string
		issues  []string
	}
	findings := map[string]finding{}
	for _, f := range audit.Findings {
		book, ok := auditLabelBook[f.Label]
		if !ok {
			continue
		}
		findings[book.String()+"/"+f.RecommendationID] = finding{verdict: f.Verdict, issues: f.Issues}
	}

	s, err := l.Settings(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	rules := s.GetSimulation()
	nowMs := l.now().UnixMilli()
	view := &leapmuxv1.TradingLabRunView{
		RunId: runID, LabelSources: labelSources, Recorded: true,
		EvaluationJson: evalF.GetCanonicalJson(), AuditJson: auditF.GetCanonicalJson(),
		BlindPredictionSha256: shas[leapmuxv1.TradingBook_TRADING_BOOK_BLIND], ContrarianPredictionSha256: shas[leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN],
		Snapshot: &leapmuxv1.TradingSnapshotSummary{
			Id: stored.id, RunId: runID, Sha256: stored.sha256, AsOfMs: stored.asOfMs, Source: stored.source, Symbols: snap.Rules.Symbols,
			Contracts: uint32(len(snap.Options)), Catalysts: uint32(len(snap.Catalysts)), Warnings: snap.Warnings, CapturedAtMs: stored.capturedAtMs,
		},
	}
	err = l.store.inTx(ctx, func(q querier) error {
		for _, book := range []leapmuxv1.TradingBook{leapmuxv1.TradingBook_TRADING_BOOK_BLIND, leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN} {
			p := preds[book]
			label := bookLabel[book]
			if p.NoTrade {
				c := &leapmuxv1.TradingCandidate{
					Id: candidateID(runID, book, "no-trade"), RunId: runID, Source: book, AnonymousLabel: label, RecommendationId: "no-trade",
					Status: leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_NO_TRADE, Thesis: p.NoTradeReason,
					PredictionSha256: shas[book], CreatedAtMs: nowMs,
				}
				if err := putCandidate(ctx, q, req.UserID, c, nowMs); err != nil {
					return err
				}
				view.Candidates = append(view.Candidates, c)
				continue
			}
			for i := range p.Recommendations {
				r := &p.Recommendations[i]
				c := candidateFrom(runID, book, label, r, shas[book], nowMs, rules)
				c.RejectionReasons = CheckRecommendation(snap, snapF.GetSha256(), p, r, rules)
				key := label + "/" + r.ID
				if total, ok := scores[key]; ok {
					c.EvaluationScore, c.EvaluationVerdict = total, verdicts[key]
				} else {
					c.RejectionReasons = append(c.RejectionReasons, "the evaluation did not score it")
				}
				if verdicts[key] == "reject" {
					c.RejectionReasons = append(c.RejectionReasons, "the evaluator rejected it")
				}
				if f, ok := findings[book.String()+"/"+r.ID]; ok {
					c.AuditVerdict, c.AuditIssues = f.verdict, f.issues
					if f.verdict == "fail" {
						c.RejectionReasons = append(c.RejectionReasons, "the risk audit failed it")
					}
				} else {
					c.RejectionReasons = append(c.RejectionReasons, "the risk audit did not cover it")
				}
				c.Status = leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE
				if len(c.RejectionReasons) > 0 {
					c.Status = leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_REJECTED
				}
				if err := putCandidate(ctx, q, req.UserID, c, nowMs); err != nil {
					return err
				}
				view.Candidates = append(view.Candidates, c)

				// The books follow the analyst, not the gate: every idea is
				// simulated, and the simulator alone decides whether the
				// recorded quote could fill it.
				occ, _ := NormalizeOCC(r.Contract.OCCSymbol)
				pos := OpenShadow(book, snap, occ, r.Entry.LimitPrice, rules)
				pos.Id, pos.CandidateId, pos.RunId, pos.ForecastProbability = positionID(c.GetId(), book), c.GetId(), runID, r.Probabilities.Profit
				if err := putPosition(ctx, q, req.UserID, pos, nowMs); err != nil {
					return err
				}
				if book == leapmuxv1.TradingBook_TRADING_BOOK_BLIND {
					inverseBook := leapmuxv1.TradingBook_TRADING_BOOK_BLIND_INVERSE
					var inv *leapmuxv1.TradingPosition
					if q2, err := InverseContract(snap, occ); err != nil {
						inv = &leapmuxv1.TradingPosition{Book: inverseBook, OccSymbol: occ, OpenedAtMs: snap.AsOf.UnixMilli(),
							State: leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_NOT_FILLED, Note: err.Error()}
					} else {
						inv = OpenShadow(inverseBook, snap, q2.OCCSymbol, 0, rules)
						inv.Note = strings.TrimSpace("Opposite of " + occ + ". " + inv.GetNote())
					}
					inv.Id, inv.CandidateId, inv.RunId = positionID(c.GetId(), inverseBook), c.GetId(), runID
					if err := putPosition(ctx, q, req.UserID, inv, nowMs); err != nil {
						return err
					}
				}
			}
		}
		for _, problem := range evalProblems {
			view.Snapshot.Warnings = append(view.Snapshot.Warnings, "evaluation: "+problem)
		}
		return insertRunView(ctx, q, req.UserID, stored.id, view, nowMs)
	})
	if err != nil {
		return nil, err
	}
	return recordResult(view), nil
}

func candidateFrom(runID string, book leapmuxv1.TradingBook, label string, r *Recommendation, predSHA string, nowMs int64, rules *leapmuxv1.TradingSimulationRules) *leapmuxv1.TradingCandidate {
	raw, _ := json.Marshal(r)
	canonical, _, _ := workflow.CanonicalJSON(string(raw))
	occ, err := NormalizeOCC(r.Contract.OCCSymbol)
	if err != nil {
		occ = r.Contract.OCCSymbol
	}
	catalyst := r.Catalyst.Type + ": " + r.Catalyst.Description
	if r.Catalyst.Date != "" {
		catalyst = r.Catalyst.Date + " " + catalyst
	}
	return &leapmuxv1.TradingCandidate{
		Id: candidateID(runID, book, r.ID), RunId: runID, Source: book, AnonymousLabel: label, RecommendationId: r.ID,
		Underlying: r.Underlying, Strategy: r.Strategy, OccSymbol: occ, Expiration: r.Contract.Expiration, Strike: r.Contract.Strike,
		Right: r.Contract.Right, EntryLimit: r.Entry.LimitPrice, MaxLossPerContract: round2(r.Entry.LimitPrice*100 + fee(rules)),
		Thesis: r.Thesis, Catalyst: catalyst, ProbabilityProfit: r.Probabilities.Profit, Probability_3X: r.Probabilities.Reach3x,
		Probability_5X: r.Probabilities.Reach5x, PredictionSha256: predSHA, RecommendationJson: canonical, CreatedAtMs: nowMs,
	}
}

func recordResult(v *leapmuxv1.TradingLabRunView) *workflow.ExecutorResult {
	counts := map[leapmuxv1.TradingCandidateStatus]int{}
	for _, c := range v.GetCandidates() {
		counts[c.GetStatus()]++
	}
	summary := map[string]any{
		"schema_version": SchemaVersion, "run_id": v.GetRunId(), "snapshot_sha256": v.GetSnapshot().GetSha256(),
		"candidates": len(v.GetCandidates()), "eligible": counts[leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE],
		"rejected": counts[leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_REJECTED],
		"no_trade": counts[leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_NO_TRADE], "label_sources": v.GetLabelSources(),
	}
	raw, _ := json.Marshal(summary)
	return &workflow.ExecutorResult{
		Summary: fmt.Sprintf("Recorded %d candidates: %d eligible for human review, %d rejected, %d no-trade. Shadow books opened; nothing was ordered.",
			len(v.GetCandidates()), summary["eligible"], summary["rejected"], summary["no_trade"]),
		StructuredOutput: string(raw),
	}
}

// --- Views ---

// RunView is what the Trading Research view shows for one run. Before the
// record step it shows the snapshot only, and never which label is whose.
func (l *Lab) RunView(ctx context.Context, userID, runID string) (*leapmuxv1.TradingLabRunView, error) {
	if v, err := getRunView(ctx, l.store.db, userID, runID); err == nil {
		cands, err := listCandidatesForRun(ctx, l.store.db, userID, runID)
		if err != nil {
			return nil, err
		}
		v.Candidates = cands
		return v, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	v := &leapmuxv1.TradingLabRunView{RunId: runID}
	if s, err := snapshotForRun(ctx, l.store.db, userID, runID); err == nil {
		snap, _ := DecodeSnapshot(s.canonical)
		v.Snapshot = &leapmuxv1.TradingSnapshotSummary{Id: s.id, RunId: runID, Sha256: s.sha256, AsOfMs: s.asOfMs, Source: s.source, CapturedAtMs: s.capturedAtMs}
		if snap != nil {
			v.Snapshot.Symbols, v.Snapshot.Contracts, v.Snapshot.Catalysts, v.Snapshot.Warnings = snap.Rules.Symbols, uint32(len(snap.Options)), uint32(len(snap.Catalysts)), snap.Warnings
		}
	}
	return v, nil
}

// DecideCandidate moves an ELIGIBLE candidate into the approved trade queue,
// or dismisses it. Approval orders nothing: an order still needs its own
// confirmation through the gateway.
func (l *Lab) DecideCandidate(ctx context.Context, userID string, req *leapmuxv1.DecideTradingCandidateRequest) (*leapmuxv1.TradingCandidate, error) {
	var out *leapmuxv1.TradingCandidate
	err := l.store.inTx(ctx, func(q querier) error {
		c, err := getCandidate(ctx, q, userID, req.GetCandidateId())
		if err != nil {
			return err
		}
		switch {
		case req.GetApprove() && c.GetStatus() != leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE:
			return conflictf("only an eligible candidate can be approved; this one is %s", strings.ToLower(strings.TrimPrefix(c.GetStatus().String(), "TRADING_CANDIDATE_STATUS_")))
		case !req.GetApprove() && c.GetStatus() != leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE &&
			c.GetStatus() != leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_APPROVED:
			return conflictf("only an eligible or approved candidate can be dismissed")
		}
		c.Status = leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_DISMISSED
		if req.GetApprove() {
			c.Status = leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_APPROVED
		}
		c.DecisionNote, c.DecidedAtMs = strings.TrimSpace(req.GetNote()), l.now().UnixMilli()
		out = c
		return putCandidate(ctx, q, userID, c, c.GetDecidedAtMs())
	})
	return out, err
}

// Dashboard gathers every book, the queue, the orders and the policies.
func (l *Lab) Dashboard(ctx context.Context, userID string) (*leapmuxv1.GetTradingDashboardResponse, error) {
	s, err := l.Settings(ctx, userID)
	if err != nil {
		return nil, err
	}
	positions, err := listPositions(ctx, l.store.db, userID)
	if err != nil {
		return nil, err
	}
	marks, err := listMarks(ctx, l.store.db, userID)
	if err != nil {
		return nil, err
	}
	out := &leapmuxv1.GetTradingDashboardResponse{Settings: s, Positions: positions}
	for _, book := range []leapmuxv1.TradingBook{
		leapmuxv1.TradingBook_TRADING_BOOK_BLIND, leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN,
		leapmuxv1.TradingBook_TRADING_BOOK_BLIND_INVERSE, leapmuxv1.TradingBook_TRADING_BOOK_LIVE,
	} {
		out.Metrics = append(out.Metrics, BookMetrics(book, positions, marks, s.GetSimulation()))
	}
	if out.Queue, err = listQueue(ctx, l.store.db, userID); err != nil {
		return nil, err
	}
	if out.Orders, err = listOrders(ctx, l.store.db, userID); err != nil {
		return nil, err
	}
	if out.Policies, err = listPolicies(ctx, l.store.db, userID); err != nil {
		return nil, err
	}
	if out.Broker, err = getBrokerCheck(ctx, l.store.db, userID); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkBooks marks every open position against the market data since its
// last mark, and lets an approved policy close a live position whose bid
// reached its bound. Simulated books are marked from the configured market
// data; live positions only from the broker, and only in live mode, so a
// replay recording can never move a real position.
func (l *Lab) MarkBooks(ctx context.Context, userID string) (*leapmuxv1.MarkTradingBooksResponse, error) {
	s, err := l.Settings(ctx, userID)
	if err != nil {
		return nil, err
	}
	positions, err := listPositions(ctx, l.store.db, userID)
	if err != nil {
		return nil, err
	}
	var shadow, live []*leapmuxv1.TradingPosition
	for _, p := range positions {
		switch {
		case p.GetState() != leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN:
		case p.GetBook() == leapmuxv1.TradingBook_TRADING_BOOK_LIVE:
			live = append(live, p)
		default:
			shadow = append(shadow, p)
		}
	}
	resp := &leapmuxv1.MarkTradingBooksResponse{}
	if len(shadow) > 0 {
		md, closeMD, err := l.marketData(ctx, userID, s)
		if err != nil {
			return nil, conflictf("market data: %v", err)
		}
		err = l.markWith(ctx, userID, md, shadow, s, resp)
		closeMD()
		if err != nil {
			return nil, err
		}
	}
	if len(live) > 0 && s.GetMode() == leapmuxv1.TradingLabMode_TRADING_LAB_MODE_LIVE {
		if sess, err := l.connect(ctx, userID, s.GetRobinhood()); err != nil {
			resp.Warnings = append(resp.Warnings, "live positions were not marked: "+err.Error())
		} else {
			err = l.markWith(ctx, userID, &LiveMarket{Broker: sess.Reader(), Now: l.now}, live, s, resp)
			_ = sess.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	resp.Warnings = append(resp.Warnings, l.runPolicies(ctx, userID, s)...)
	return resp, nil
}

func (l *Lab) markWith(ctx context.Context, userID string, md MarketData, open []*leapmuxv1.TradingPosition, s *leapmuxv1.TradingLabSettings, resp *leapmuxv1.MarkTradingBooksResponse) error {
	var contracts, symbols []string
	since := time.Time{}
	for _, p := range open {
		if !slices.Contains(contracts, p.GetOccSymbol()) {
			contracts = append(contracts, p.GetOccSymbol())
		}
		if !slices.Contains(symbols, p.GetUnderlying()) {
			symbols = append(symbols, p.GetUnderlying())
		}
		if last := time.UnixMilli(p.GetLastMarkAtMs()); since.IsZero() || last.Before(since) {
			since = last
		}
	}
	frames, err := md.FramesSince(ctx, since, contracts, symbols)
	if err != nil {
		return conflictf("market data: %v", err)
	}
	if len(frames) == 0 {
		resp.Warnings = append(resp.Warnings, "no market data newer than the last marks")
		return nil
	}
	nowMs := l.now().UnixMilli()
	return l.store.inTx(ctx, func(q querier) error {
		for _, p := range open {
			for _, f := range frames {
				marks := MarkPosition(p, f, s.GetSimulation())
				for _, m := range marks {
					if m.Missing {
						resp.Missing++
					} else if m.HorizonDays == 0 {
						resp.Marked++
					}
				}
				if err := insertMarks(ctx, q, p.GetId(), marks); err != nil {
					return err
				}
			}
			if err := putPosition(ctx, q, userID, p, nowMs); err != nil {
				return err
			}
		}
		return nil
	})
}
