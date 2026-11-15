package tradinglab

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/mcpclient"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// A workflow step whose `tools` param is "market" may call the broker's
// research tools through the lab (leapmux control workflow tools): the
// agent reads the server's own descriptions and schemas, so it adapts when
// the broker changes them. The lab holds the sign-in and forwards only the
// tools on marketReadTools; every account, position, order, bank, alert and
// watchlist tool is absent, so no instruction or injected text can reach
// one. Every call is logged per task (trading_market_calls).
const (
	StepToolsParam = "tools"
	ToolsMarket    = "market"
)

// marketReadTools are the broker tools research may call: market data and
// public company information on Robinhood's agent server. A tool not on the
// list is never offered, so a tool the broker adds stays unreachable until
// it is reviewed here.
var marketReadTools = []string{
	"get_equity_quotes", "get_equity_historicals", "get_equity_fundamentals", "get_equity_analyst_ratings",
	"get_equity_technical_indicators", "get_equity_tradability", "get_equity_price_book",
	"get_earnings_calendar", "get_earnings_results", "get_financials",
	"get_index_quotes", "get_index_historicals", "get_indexes",
	"get_option_chains", "get_option_instruments", "get_option_quotes", "get_option_historicals",
	"get_sec_filing", "get_sec_filing_facts", "get_sec_filing_facts_catalog", "get_sec_filing_index",
	"get_crypto_quotes", "get_currency_pairs",
	"get_scanner_datapoints", "get_scanner_filter_specs", "get_scans", "run_scan", "preview_scan",
	"get_popular_watchlists", "get_politician_trades", "search",
}

const (
	// scopeTool is the lab's own tool beside the broker's.
	scopeTool     = "lab_research_scope"
	defaultMaxDTE = 14

	marketSessionIdle = 5 * time.Minute
	marketLogLimit    = 512 << 10
	marketResultLimit = 4 << 20
)

// MarketTool is a research tool as an agent sees it.
type MarketTool struct {
	Name, Description, InputSchema string
}

// marketSession keeps one broker session for the agents' calls, so a step
// that makes dozens of calls does not reconnect for each.
type marketSession struct {
	mu   sync.Mutex
	key  string
	sess BrokerSession
	idle *time.Timer
}

func (l *Lab) marketBroker(ctx context.Context, userID string) (BrokerSession, error) {
	s, err := l.Settings(ctx, userID)
	if err != nil {
		return nil, err
	}
	if s.GetMarketData() != leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_ROBINHOOD {
		return nil, conflictf("the lab uses recorded market data, so there are no live market tools; set the market data source to Robinhood")
	}
	key := userID + "\x00" + s.GetRobinhood().GetUrl()
	m := &l.market
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess != nil && m.key != key {
		_ = m.sess.Close()
		m.sess = nil
	}
	if m.sess == nil {
		sess, err := l.connect(ctx, userID, s.GetRobinhood())
		if err != nil {
			return nil, conflictf("the broker is unreachable: %v", err)
		}
		m.sess, m.key = sess, key
	}
	if m.idle != nil {
		m.idle.Stop()
	}
	sess := m.sess
	m.idle = time.AfterFunc(marketSessionIdle, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.sess == sess {
			_ = m.sess.Close()
			m.sess = nil
		}
	})
	return sess, nil
}

// dropMarketSession forgets a session that failed, so the next call
// reconnects.
func (l *Lab) dropMarketSession(sess BrokerSession) {
	m := &l.market
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == sess {
		_ = m.sess.Close()
		m.sess = nil
	}
}

func marketToolAllowed(t mcpclient.Tool) bool {
	if !slices.Contains(marketReadTools, t.Name) {
		return false
	}
	return t.Annotations == nil || t.Annotations.DestructiveHint == nil || !*t.Annotations.DestructiveHint
}

// MarketTools lists the research tools the broker offers.
func (l *Lab) MarketTools(ctx context.Context, userID string) ([]MarketTool, error) {
	sess, err := l.marketBroker(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := []MarketTool{{
		Name:        scopeTool,
		Description: "The lab's research scope: the watchlist of underlyings to cover and how far out expirations may be. Call it first.",
		InputSchema: `{"type":"object","properties":{},"additionalProperties":false}`,
	}}
	for _, t := range sess.Tools() {
		if marketToolAllowed(t) {
			out = append(out, MarketTool{Name: t.Name, Description: t.Description, InputSchema: string(t.InputSchema)})
		}
	}
	return out, nil
}

// MarketCall calls one research tool for a run's task and logs it. A tool
// off the list is refused as though it did not exist.
func (l *Lab) MarketCall(ctx context.Context, userID, runID, taskID, tool, argumentsJSON string) (string, bool, error) {
	args := map[string]any{}
	if a := strings.TrimSpace(argumentsJSON); a != "" {
		if err := json.Unmarshal([]byte(a), &args); err != nil {
			return "", false, invalidf("the arguments must be a JSON object: %v", err)
		}
	}
	if tool == scopeTool {
		s, err := l.Settings(ctx, userID)
		if err != nil {
			return "", false, err
		}
		scope, _ := json.Marshal(map[string]any{"watchlist": s.GetWatchlist(), "max_days_to_expiry": defaultMaxDTE})
		return string(scope), false, nil
	}
	for attempt := 0; ; attempt++ {
		sess, err := l.marketBroker(ctx, userID)
		if err != nil {
			return "", false, err
		}
		i := slices.IndexFunc(sess.Tools(), func(t mcpclient.Tool) bool { return t.Name == tool })
		if i < 0 || !marketToolAllowed(sess.Tools()[i]) {
			return "", false, deniedf("%q is not one of the research tools; list them with `leapmux control workflow tools`", tool)
		}
		res, err := sess.CallTool(ctx, tool, args)
		if err != nil && res == nil {
			l.dropMarketSession(sess)
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			return "", false, conflictf("the broker call failed: %v", err)
		}
		text, isError := res.Text(), res.IsError
		if len(text) > marketResultLimit {
			text = text[:marketResultLimit] + "\n[truncated: narrow the request]"
		}
		logged := text
		if len(logged) > marketLogLimit {
			logged = logged[:marketLogLimit]
		}
		argsJSON, _ := json.Marshal(args)
		if _, err := l.store.db.ExecContext(ctx, `INSERT INTO trading_market_calls (user_id, run_id, task_id, tool, arguments, result, is_error, at_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, userID, runID, taskID, tool, string(argsJSON), logged, isError, l.now().UnixMilli()); err != nil {
			return "", false, fmt.Errorf("log the market call: %w", err)
		}
		return text, isError, nil
	}
}

// marketCallsForRun counts a run's successful research-tool calls by tool.
func (l *Lab) marketCallsForRun(ctx context.Context, runID string) (map[string]int, error) {
	rows, err := l.store.db.QueryContext(ctx, `SELECT tool, COUNT(*) FROM trading_market_calls WHERE run_id = ? AND is_error = 0 GROUP BY tool`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var tool string
		var n int
		if err := rows.Scan(&tool, &n); err != nil {
			return nil, err
		}
		out[tool] = n
	}
	return out, rows.Err()
}

// marketSelection is the market-data step's frozen output
// (schemas/market_selection.schema.json).
type marketSelection struct {
	Symbols   []string `json:"symbols"`
	Contracts []struct {
		InstrumentID string `json:"instrument_id"`
	} `json:"contracts"`
	Catalysts []struct {
		Symbol string `json:"symbol"`
		Type   string `json:"type"`
		Date   string `json:"date"`
		Title  string `json:"title"`
		Tool   string `json:"tool"`
	} `json:"catalysts"`
}

func (m *marketSelection) contractIDs() []string {
	var out []string
	for _, c := range m.Contracts {
		if !slices.Contains(out, c.InstrumentID) {
			out = append(out, c.InstrumentID)
		}
	}
	return out
}

// catalysts keeps the selection's catalysts that cite a research tool the
// run actually called: a fact the agent attributes to a tool it never used
// is dropped rather than shown to the analysts.
func (m *marketSelection) catalysts(calls map[string]int) []Catalyst {
	var out []Catalyst
	for _, c := range m.Catalysts {
		if calls[c.Tool] == 0 {
			continue
		}
		out = append(out, Catalyst{Symbol: c.Symbol, Type: c.Type, Date: c.Date, Title: c.Title, Source: "broker:" + c.Tool + " (selected by the market-data step)"})
	}
	return out
}

// selectionFor is the frozen selection of the step the snapshot step names
// (its selection_step param), or nil when it names none.
func selectionFor(req workflow.ExecutorRequest) (*marketSelection, error) {
	key := param(req.Step, "selection_step", "")
	if key == "" {
		return nil, nil
	}
	f := frozenByStep(req.Frozen)[key]
	if f == nil {
		return nil, fmt.Errorf("the market-data step %q has no frozen result", key)
	}
	var sel marketSelection
	if err := json.Unmarshal([]byte(f.GetCanonicalJson()), &sel); err != nil {
		return nil, fmt.Errorf("read the market selection: %w", err)
	}
	return &sel, nil
}
