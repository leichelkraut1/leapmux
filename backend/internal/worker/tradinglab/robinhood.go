package tradinglab

import (
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/mcpclient"
)

// The logical operations the lab needs from a broker's MCP server. A
// server's tool for each is found by name (TradingMcpConfig.tool_names, else
// the first of defaultToolNames that exists).
const (
	OpQuote        = "quote"
	OpOptionChain  = "option_chain"
	OpOptionQuotes = "option_quotes"
	// OpOptionInstruments lists contracts by underlying and expiration, where
	// a server addresses contracts by id rather than by OCC symbol.
	OpOptionInstruments = "option_instruments"
	// OpBuyingPower reads the account's buying power.
	OpBuyingPower  = "buying_power"
	OpNews         = "news"
	OpEvents       = "events"
	OpAccount      = "account"
	OpPositions    = "positions"
	OpOrders       = "orders"
	OpOrderStatus  = "order_status"
	OpPreviewOrder = "preview_order"
	OpPlaceOrder   = "place_order"
	OpCancelOrder  = "cancel_order"
)

// readOps are the only operations research and the snapshot may use.
var readOps = []string{OpQuote, OpOptionChain, OpOptionInstruments, OpOptionQuotes, OpNews, OpEvents, OpAccount, OpBuyingPower, OpPositions, OpOrders, OpOrderStatus}

// tradeOps are reachable only through TradingBroker, which only the gateway
// holds.
var tradeOps = []string{OpPreviewOrder, OpPlaceOrder, OpCancelOrder}

// requiredForResearch and requiredForLive are what each mode cannot do without.
var (
	requiredForResearch = []string{OpQuote, OpOptionChain, OpOptionInstruments, OpOptionQuotes}
	requiredForLive     = []string{OpAccount, OpOptionQuotes, OpPreviewOrder, OpPlaceOrder, OpCancelOrder, OpOrderStatus, OpOrders}
)

// defaultToolNames are Robinhood's agent MCP server's tool names (read from
// the live server on 2026-11-14). get_option_orders serves both a list and
// one order.
var defaultToolNames = map[string][]string{
	OpQuote:             {"get_equity_quotes"},
	OpOptionChain:       {"get_option_chains"},
	OpOptionInstruments: {"get_option_instruments"},
	OpOptionQuotes:      {"get_option_quotes"},
	OpNews:              {},
	OpEvents:            {"get_earnings_results"},
	OpAccount:           {"get_accounts"},
	OpBuyingPower:       {"get_portfolio"},
	OpPositions:         {"get_option_positions"},
	OpOrders:            {"get_option_orders"},
	OpOrderStatus:       {"get_option_orders"},
	OpPreviewOrder:      {"review_option_order"},
	OpPlaceOrder:        {"place_option_order"},
	OpCancelOrder:       {"cancel_option_order"},
}

// writeLike names a tool that changes something. A read operation mapped to
// such a tool is refused: a wrong mapping must not hand research an order
// tool under a harmless name.
var writeLike = regexp.MustCompile(`(?i)(place|submit|cancel|replace|modify|transfer|withdraw|deposit|buy|sell|trade|execute|create)`)

// McpDialer opens an MCP session. Tests replace it.
type McpDialer func(ctx context.Context, cfg mcpclient.Config) (*mcpclient.Client, error)

// ResolveToken reads the bearer token from where the settings point. The
// token is never stored or logged.
func ResolveToken(cfg *leapmuxv1.TradingMcpConfig) (string, error) {
	switch {
	case cfg.GetTokenEnv() != "":
		v := strings.TrimSpace(os.Getenv(cfg.GetTokenEnv()))
		if v == "" {
			return "", fmt.Errorf("the environment variable %s is not set on this computer", cfg.GetTokenEnv())
		}
		return v, nil
	case cfg.GetTokenFile() != "":
		raw, err := os.ReadFile(cfg.GetTokenFile())
		if err != nil {
			return "", fmt.Errorf("read the token file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	default:
		return "", nil
	}
}

// Robinhood is a verified session with a Robinhood MCP server. It hands out
// two views: Reader (read operations only) and Trader (the gateway's).
type Robinhood struct {
	client    *mcpclient.Client
	tools     map[string]string // op -> tool name
	all       []mcpclient.Tool
	refused   []string
	accountID string
	server    string

	mu sync.Mutex
	// contracts are the contracts the session has seen, by OCC symbol: the
	// server addresses a contract by its instrument id.
	contracts map[string]contract
	// account is the account number the trading tools address, once read.
	account string
}

// ConnectRobinhood dials the configured server and classifies its tools.
//
// With SIGN_IN auth, tokens supplies the access token of the person's
// browser sign-in, refreshed as it nears expiry; otherwise the token comes
// from the configured environment variable or file.
func ConnectRobinhood(ctx context.Context, cfg *leapmuxv1.TradingMcpConfig, dial McpDialer, tokens func(context.Context) (string, error)) (*Robinhood, error) {
	if cfg == nil || (cfg.GetUrl() == "" && cfg.GetCommand() == "") {
		return nil, errors.New("no Robinhood MCP server is configured")
	}
	mc := mcpclient.Config{URL: cfg.GetUrl(), Command: cfg.GetCommand(), Args: cfg.GetArgs(), ClientName: "leapmux-tradinglab"}
	if cfg.GetAuth() == leapmuxv1.TradingBrokerAuth_TRADING_BROKER_AUTH_SIGN_IN {
		if tokens == nil || cfg.GetUrl() == "" {
			return nil, errors.New("signing in to the broker needs its HTTP URL")
		}
		mc.TokenSource = tokens
	} else {
		token, err := ResolveToken(cfg)
		if err != nil {
			return nil, err
		}
		mc.BearerToken = token
	}
	if cfg.GetTransport() == leapmuxv1.TradingMcpTransport_TRADING_MCP_TRANSPORT_STDIO {
		mc.URL = ""
	} else if cfg.GetTransport() == leapmuxv1.TradingMcpTransport_TRADING_MCP_TRANSPORT_HTTP {
		mc.Command = ""
	}
	if dial == nil {
		dial = mcpclient.Dial
	}
	client, err := dial(ctx, mc)
	if err != nil {
		return nil, err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("list the server's tools: %w", err)
	}
	r := &Robinhood{client: client, tools: map[string]string{}, all: tools, accountID: cfg.GetAccountId(), server: client.Server.Name}
	byName := map[string]mcpclient.Tool{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	for _, op := range append(slices.Clone(readOps), tradeOps...) {
		candidates := defaultToolNames[op]
		if name := strings.TrimSpace(cfg.GetToolNames()[op]); name != "" {
			candidates = []string{name}
		}
		for _, name := range candidates {
			t, ok := byName[name]
			if !ok {
				continue
			}
			if slices.Contains(readOps, op) && (writeLike.MatchString(name) && !strings.HasPrefix(name, "get_") ||
				t.Annotations != nil && t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint) {
				r.refused = append(r.refused, fmt.Sprintf("%s is mapped to %s, which looks like it changes something; refused", op, name))
				break
			}
			r.tools[op] = name
			break
		}
	}
	return r, nil
}

// Close ends the session.
func (r *Robinhood) Close() error { return r.client.Close() }

// Tools lists every tool the server offers, as it described them.
func (r *Robinhood) Tools() []mcpclient.Tool { return r.all }

// CallTool calls a tool by name. Only the lab's research tools path, which
// filters by name, calls it with a name that is not one of the operations.
func (r *Robinhood) CallTool(ctx context.Context, name string, args map[string]any) (*mcpclient.CallResult, error) {
	return r.client.CallTool(ctx, name, args)
}

// Supports reports whether the server provides an operation.
func (r *Robinhood) Supports(op string) bool { return r.tools[op] != "" }

func (r *Robinhood) callJSON(ctx context.Context, op string, args map[string]any) (any, error) {
	name := r.tools[op]
	if name == "" {
		return nil, fmt.Errorf("the Robinhood MCP server has no tool for %s", op)
	}
	res, err := r.client.CallTool(ctx, name, args)
	if err != nil {
		return nil, err
	}
	raw, err := res.JSON()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// Status verifies the session: which operations exist, and whether an
// account read succeeds with the credential, which is the only proof of
// authentication a server gives.
func (r *Robinhood) Status(ctx context.Context, now time.Time) *leapmuxv1.TradingBrokerStatus {
	st := &leapmuxv1.TradingBrokerStatus{Configured: true, CheckedAtMs: now.UnixMilli()}
	classified := map[string]bool{}
	for _, op := range append(slices.Clone(readOps), tradeOps...) {
		if name := r.tools[op]; name != "" {
			st.SupportedOperations = append(st.SupportedOperations, op)
			classified[name] = true
			if slices.Contains(tradeOps, op) {
				st.TradingTools = append(st.TradingTools, name)
			} else {
				st.ReadOnlyTools = append(st.ReadOnlyTools, name)
			}
		} else {
			st.MissingOperations = append(st.MissingOperations, op)
		}
	}
	for _, t := range r.all {
		if !classified[t.Name] {
			st.UnclassifiedTools = append(st.UnclassifiedTools, t.Name)
		}
	}
	var notes []string
	notes = append(notes, r.refused...)
	if acct, err := r.Account(ctx); err != nil {
		notes = append(notes, "account read failed: "+err.Error())
	} else {
		st.Authenticated = true
		st.AccountLabel, st.OptionsLevel, st.BuyingPower = maskAccount(acct.ID), acct.OptionsLevel, acct.BuyingPower
	}
	var missing []string
	for _, op := range requiredForLive {
		if !r.Supports(op) {
			missing = append(missing, op)
		}
	}
	switch {
	case !st.Authenticated:
		notes = append(notes, "live trading needs an authenticated account read")
	case len(missing) > 0:
		notes = append(notes, "live trading needs "+strings.Join(missing, ", "))
	case !optionsLevelAllowsLong(st.OptionsLevel):
		notes = append(notes, fmt.Sprintf("options level %q does not allow buying calls and puts", st.OptionsLevel))
	default:
		st.LiveReady = true
	}
	for _, op := range requiredForResearch {
		if !r.Supports(op) {
			notes = append(notes, "research market data needs "+op)
		}
	}
	st.Detail = strings.Join(notes, "; ")
	return st
}

func maskAccount(id string) string {
	if len(id) <= 4 {
		return id
	}
	return strings.Repeat("*", len(id)-4) + id[len(id)-4:]
}

// optionsLevelAllowsLong reads Robinhood's option levels ("option_level_2",
// "2", "level 3") as allowing long calls and puts from level 2.
func optionsLevelAllowsLong(level string) bool {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, level)
	n, err := strconv.Atoi(digits)
	return err == nil && n >= 2
}

// --- Robinhood's replies ---
//
// Robinhood's agent MCP server (agent.robinhood.com/mcp/trading) answers
// every tool with {"data": {...}, "guide": "..."}: quotes and option quotes
// as data.results[].quote, chains as data.chains, contracts as
// data.instruments (paged by data.next), accounts as data.accounts and
// orders as data.orders. Contracts are addressed by instrument UUID, so the
// adapter looks each OCC symbol up once and keeps its id. The field names
// below are the ones its replies carry; the shapes were read from the live
// server on 2026-11-14.

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// payload unwraps the {"data": ...} envelope.
func payload(v any) map[string]any {
	m := asMap(v)
	if d := asMap(m["data"]); d != nil {
		return d
	}
	return m
}

func listOf(v any, key string) []map[string]any {
	var out []map[string]any
	for _, item := range asList(payload(v)[key]) {
		if m := asMap(item); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// quoteOf is a results[] entry's quote: {"quote": {...}, "close": {...}}.
func quoteOf(m map[string]any) map[string]any {
	if q := asMap(m["quote"]); q != nil {
		return q
	}
	return m
}

func asList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case map[string]any:
		return []any{t}
	default:
		return nil
	}
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case json.Number:
			return v.String()
		}
	}
	return ""
}

func num(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f
			}
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
		case float64:
			return v
		}
	}
	return 0
}

// findNum is the first number under a key matching want, at any depth.
func findNum(v any, want func(string) bool) (float64, bool) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if want(k) {
				if f := num(t, k); f != 0 {
					return f, true
				}
				if inner, ok := findNum(t[k], func(string) bool { return true }); ok {
					return inner, true
				}
			}
		}
		for _, k := range keys {
			if f, ok := findNum(t[k], want); ok {
				return f, true
			}
		}
	case []any:
		for _, item := range t {
			if f, ok := findNum(item, want); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func when(m map[string]any, keys ...string) time.Time {
	for _, k := range keys {
		if s := str(m, k); s != "" {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
				if t, err := time.Parse(layout, s); err == nil {
					return t.UTC()
				}
			}
		}
	}
	return time.Time{}
}

func boolOf(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

// --- Read operations ---

// Account is what the gateway needs to know about the brokerage account.
type Account struct {
	ID           string
	BuyingPower  float64
	OptionsLevel string
}

// Account reads the account the lab trades in: the configured one, else the
// one account Robinhood lets an agent trade (agentic_allowed). Its buying
// power comes from the portfolio, since the account list does not carry a
// reliable one.
func (r *Robinhood) Account(ctx context.Context) (*Account, error) {
	v, err := r.callJSON(ctx, OpAccount, map[string]any{})
	if err != nil {
		return nil, err
	}
	accounts := listOf(v, "accounts")
	var chosen map[string]any
	for _, m := range accounts {
		id := str(m, "account_number")
		if r.accountID != "" && id == r.accountID || r.accountID == "" && boolOf(m, "agentic_allowed") {
			chosen = m
			break
		}
	}
	if chosen == nil {
		if r.accountID != "" {
			return nil, fmt.Errorf("account %s is not among the server's accounts", maskAccount(r.accountID))
		}
		return nil, errors.New("no account is enabled for agent trading; enable one in the Robinhood app")
	}
	a := &Account{ID: str(chosen, "account_number"), OptionsLevel: str(chosen, "option_level")}
	if a.ID == "" {
		return nil, errors.New("the account read returned no account number")
	}
	if r.Supports(OpBuyingPower) {
		if p, err := r.callJSON(ctx, OpBuyingPower, map[string]any{"account_number": a.ID}); err == nil {
			a.BuyingPower, _ = findNum(payload(p), func(k string) bool { return strings.Contains(k, "buying_power") })
		}
	}
	r.account = a.ID
	return a, nil
}

// accountNumber is the account the trading tools address.
func (r *Robinhood) accountNumber(ctx context.Context) (string, error) {
	if r.account != "" {
		return r.account, nil
	}
	a, err := r.Account(ctx)
	if err != nil {
		return "", err
	}
	return a.ID, nil
}

func (r *Robinhood) Quotes(ctx context.Context, symbols []string) ([]UnderlyingQuote, error) {
	v, err := r.callJSON(ctx, OpQuote, map[string]any{"symbols": symbols})
	if err != nil {
		return nil, err
	}
	var out []UnderlyingQuote
	for _, item := range listOf(v, "results") {
		m := quoteOf(item)
		sym := strings.ToUpper(str(m, "symbol"))
		if sym == "" {
			continue
		}
		// The newest trade is the last price: after the close that is an
		// extended-hours trade, when there was one.
		last := num(m, "last_trade_price", "last_non_reg_trade_price")
		if p := num(m, "last_non_reg_trade_price"); p > 0 && when(m, "venue_last_non_reg_trade_time").After(when(m, "venue_last_trade_time")) {
			last = p
		}
		out = append(out, UnderlyingQuote{
			Symbol: sym, Last: last,
			Bid: num(m, "bid_price"), Ask: num(m, "ask_price"), PrevClose: num(m, "adjusted_previous_close", "previous_close"),
			QuoteTime: latest(when(m, "venue_bid_time"), when(m, "venue_ask_time"), when(m, "venue_last_trade_time"), when(m, "venue_last_non_reg_trade_time"), when(m, "updated_at")),
		})
	}
	return out, nil
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// contract is one listed option as get_option_instruments describes it.
type contract struct {
	id, occ, underlying, expiration, right string
	strike                                 float64
	multiplier                             int
	tradable                               bool
}

func contractFrom(m map[string]any) (contract, bool) {
	c := contract{
		id: str(m, "id"), underlying: strings.ToUpper(str(m, "chain_symbol")), expiration: str(m, "expiration_date"),
		right: strings.ToLower(str(m, "type")), strike: num(m, "strike_price"), multiplier: int(num(m, "trade_value_multiplier")),
		tradable: str(m, "tradability") == "tradable" && str(m, "state") == "active",
	}
	occ, err := OCCSymbol(c.underlying, c.expiration, c.right, c.strike)
	if err != nil || c.id == "" {
		return c, false
	}
	c.occ = occ
	if c.multiplier == 0 {
		c.multiplier = 100
	}
	return c, true
}

// Limits on one chain capture: Robinhood pages contracts 100 at a time and
// quotes them 20 at a time, so a snapshot keeps to the nearest expirations
// and the strikes around the money.
const (
	chainMaxExpirations = 4
	chainStrikesPerSide = 8 // per expiration and right, nearest the money
	instrumentPages     = 10
	quoteBatch          = 20
)

// instruments lists contracts for one expiration and right, every page.
func (r *Robinhood) instruments(ctx context.Context, args map[string]any) ([]contract, error) {
	var out []contract
	for page := 0; page < instrumentPages; page++ {
		v, err := r.callJSON(ctx, OpOptionInstruments, args)
		if err != nil {
			return nil, err
		}
		for _, m := range listOf(v, "instruments") {
			if c, ok := contractFrom(m); ok {
				out = append(out, c)
				r.rememberContract(c)
			}
		}
		next := str(payload(v), "next")
		if next == "" {
			break
		}
		args = maps.Clone(args)
		args["cursor"] = next
	}
	return out, nil
}

func (r *Robinhood) rememberContract(c contract) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.contracts == nil {
		r.contracts = map[string]contract{}
	}
	r.contracts[c.occ] = c
}

// contractFor finds an OCC symbol's contract, asking the server once.
func (r *Robinhood) contractFor(ctx context.Context, occ string) (contract, error) {
	r.mu.Lock()
	c, ok := r.contracts[occ]
	r.mu.Unlock()
	if ok {
		return c, nil
	}
	p, err := ParseOCC(occ)
	if err != nil {
		return contract{}, err
	}
	found, err := r.instruments(ctx, map[string]any{
		"chain_symbol": p.Root, "expiration_dates": p.Expiration, "type": p.Right,
		"strike_price": strconv.FormatFloat(p.Strike, 'f', 4, 64),
	})
	if err != nil {
		return contract{}, err
	}
	for _, c := range found {
		if c.occ == occ {
			return c, nil
		}
	}
	return contract{}, fmt.Errorf("the broker does not list %s", occ)
}

// quoteContracts quotes contracts by instrument id, 20 at a time.
func (r *Robinhood) quoteContracts(ctx context.Context, cs []contract) ([]OptionQuote, error) {
	byID := map[string]contract{}
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		if _, dup := byID[c.id]; !dup {
			byID[c.id] = c
			ids = append(ids, c.id)
		}
	}
	var out []OptionQuote
	for start := 0; start < len(ids); start += quoteBatch {
		batch := ids[start:min(start+quoteBatch, len(ids))]
		v, err := r.callJSON(ctx, OpOptionQuotes, map[string]any{"instrument_ids": batch})
		if err != nil {
			return nil, err
		}
		for _, item := range listOf(v, "results") {
			m := quoteOf(item)
			c, ok := byID[str(m, "instrument_id")]
			if !ok {
				continue
			}
			out = append(out, OptionQuote{
				OCCSymbol: c.occ, Underlying: c.underlying, Expiration: c.expiration, Strike: c.strike, Right: c.right, Multiplier: c.multiplier,
				Bid: num(m, "bid_price"), Ask: num(m, "ask_price"), Last: num(m, "last_trade_price"),
				IV: num(m, "implied_volatility"), Delta: num(m, "delta"), Gamma: num(m, "gamma"), Theta: num(m, "theta"), Vega: num(m, "vega"),
				OpenInterest: int64(num(m, "open_interest")), Volume: int64(num(m, "volume")),
				QuoteTime: when(m, "updated_at"), Tradable: c.tradable,
			})
		}
	}
	return out, nil
}

// OptionChain captures the contracts near the money for the nearest
// expirations within maxDTE days: the chain's expirations, then each
// expiration's calls and puts, then their quotes.
func (r *Robinhood) OptionChain(ctx context.Context, symbol string, maxDTE int, asOf time.Time) ([]OptionQuote, error) {
	symbol = strings.ToUpper(symbol)
	v, err := r.callJSON(ctx, OpOptionChain, map[string]any{"underlying_symbol": symbol})
	if err != nil {
		return nil, err
	}
	today := asOf.Format(time.DateOnly)
	last := asOf.AddDate(0, 0, max(maxDTE, 1)).Format(time.DateOnly)
	var expirations []string
	for _, ch := range listOf(v, "chains") {
		if !strings.EqualFold(str(ch, "symbol"), symbol) {
			continue
		}
		for _, e := range asList(ch["expiration_dates"]) {
			if d, _ := e.(string); d >= today && d <= last && !slices.Contains(expirations, d) {
				expirations = append(expirations, d)
			}
		}
	}
	slices.Sort(expirations)
	if len(expirations) > chainMaxExpirations {
		expirations = expirations[:chainMaxExpirations]
	}
	if len(expirations) == 0 {
		return nil, nil
	}
	spot := 0.0
	if qs, err := r.Quotes(ctx, []string{symbol}); err == nil && len(qs) > 0 {
		spot = qs[0].Last
		if qs[0].Bid > 0 && qs[0].Ask > 0 {
			spot = (qs[0].Bid + qs[0].Ask) / 2
		}
	}
	var picked []contract
	for _, exp := range expirations {
		for _, right := range []string{"call", "put"} {
			cs, err := r.instruments(ctx, map[string]any{"chain_symbol": symbol, "expiration_dates": exp, "type": right, "state": "active"})
			if err != nil {
				return nil, err
			}
			picked = append(picked, nearestStrikes(cs, spot, chainStrikesPerSide)...)
		}
	}
	return r.quoteContracts(ctx, picked)
}

// nearestStrikes keeps the n contracts on each side of spot.
func nearestStrikes(cs []contract, spot float64, n int) []contract {
	if spot <= 0 || len(cs) <= 2*n {
		return cs
	}
	sorted := slices.Clone(cs)
	slices.SortFunc(sorted, func(a, b contract) int {
		return cmp.Compare(math.Abs(a.strike-spot), math.Abs(b.strike-spot))
	})
	return sorted[:2*n]
}

// OptionQuotesByID quotes contracts by the broker's instrument id, as the
// market-data step selects them.
func (r *Robinhood) OptionQuotesByID(ctx context.Context, ids []string) ([]OptionQuote, error) {
	var cs []contract
	for start := 0; start < len(ids); start += 50 {
		batch := ids[start:min(start+50, len(ids))]
		found, err := r.instruments(ctx, map[string]any{"ids": strings.Join(batch, ",")})
		if err != nil {
			return nil, err
		}
		cs = append(cs, found...)
	}
	return r.quoteContracts(ctx, cs)
}

func (r *Robinhood) OptionQuotes(ctx context.Context, occs []string) ([]OptionQuote, error) {
	var cs []contract
	for _, occ := range occs {
		norm, err := NormalizeOCC(occ)
		if err != nil {
			return nil, err
		}
		c, err := r.contractFor(ctx, norm)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return r.quoteContracts(ctx, cs)
}

// Catalysts gathers each underlying's upcoming earnings where the server
// offers them; Robinhood's server has no news tool.
func (r *Robinhood) Catalysts(ctx context.Context, symbols []string) ([]Catalyst, []string) {
	var out []Catalyst
	var warnings []string
	if !r.Supports(OpNews) {
		warnings = append(warnings, "the broker's MCP server offers no news")
	}
	if !r.Supports(OpEvents) {
		return out, append(warnings, "the broker's MCP server offers no events")
	}
	today := time.Now().UTC().Format(time.DateOnly)
	for _, sym := range symbols {
		v, err := r.callJSON(ctx, OpEvents, map[string]any{"symbol": sym})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("events for %s failed: %v", sym, err))
			continue
		}
		d := payload(v)
		var rows []map[string]any
		for _, key := range []string{"results", "earnings", "events"} {
			rows = append(rows, listOf(d, key)...)
		}
		for _, m := range rows {
			report := asMap(m["report"])
			date := str(report, "date")
			if date == "" {
				date = str(m, "report_date", "date")
			}
			if len(date) > 10 {
				date = date[:10]
			}
			if date == "" || date < today {
				continue
			}
			title := "Earnings report"
			if q, y := str(m, "quarter"), str(m, "year"); q != "" && y != "" {
				title = fmt.Sprintf("Q%s %s earnings report", q, y)
			}
			if timing := str(report, "timing"); timing != "" {
				title += " (" + timing + ")"
			}
			out = append(out, Catalyst{Symbol: sym, Type: "earnings", Date: date, Title: title, Source: "robinhood"})
		}
	}
	return out, warnings
}

// --- Trading operations (gateway only) ---

// OrderRequest is an exact options order.
type OrderRequest struct {
	AccountID     string
	OCCSymbol     string
	Side          leapmuxv1.TradingOrderSide
	Quantity      uint32
	LimitPrice    float64
	TimeInForce   string
	ClientOrderID string
}

// refID turns the gateway's client order id into the UUID Robinhood's
// ref_id idempotency key must be, the same one for every retry.
func refID(clientOrderID string) string {
	sum := sha1.Sum([]byte("leapmux-trading-order:" + clientOrderID))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// args are the review and place arguments: one leg, a limit price.
func (r *Robinhood) orderArgs(ctx context.Context, o OrderRequest) (map[string]any, error) {
	c, err := r.contractFor(ctx, o.OCCSymbol)
	if err != nil {
		return nil, err
	}
	side, effect := "buy", "open"
	if o.Side == leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_SELL_TO_CLOSE {
		side, effect = "sell", "close"
	}
	account := o.AccountID
	if account == "" {
		if account, err = r.accountNumber(ctx); err != nil {
			return nil, err
		}
	}
	args := map[string]any{
		"account_number": account,
		"legs":           []any{map[string]any{"option_id": c.id, "side": side, "position_effect": effect}},
		"type":           "limit", "quantity": strconv.FormatUint(uint64(o.Quantity), 10),
		"price": strconv.FormatFloat(o.LimitPrice, 'f', 2, 64),
	}
	if o.TimeInForce != "" {
		args["time_in_force"] = o.TimeInForce
	}
	if o.ClientOrderID != "" {
		args["ref_id"] = refID(o.ClientOrderID)
	}
	return args, nil
}

// Preview is the broker's review of an order before it is placed.
type Preview struct {
	EstimatedFees float64
	EstimatedCost float64
	Alerts        []string
	// ApprovalInApp is set when Robinhood will hold the order for the
	// customer to approve in its app once it is placed.
	ApprovalInApp bool
}

// BrokerOrder is an order as the broker reports it.
type BrokerOrder struct {
	ID            string
	ClientOrderID string
	State         leapmuxv1.TradingOrderState
	FilledQty     uint32
	AveragePrice  float64
	Fills         []BrokerFill
	Detail        string
}

// BrokerFill is one execution.
type BrokerFill struct {
	Key      string
	Quantity uint32
	Price    float64
	Fees     float64
	At       time.Time
}

// ErrRejected is the broker refusing an order outright, as opposed to a
// failure whose outcome is unknown.
var ErrRejected = errors.New("the broker rejected the order")

// alertTexts collects the human-readable alerts a review carries.
func alertTexts(v any) []string {
	var out []string
	for _, a := range asList(v) {
		switch t := a.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			if s := str(t, "message", "description", "text", "title", "alert"); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func (r *Robinhood) Preview(ctx context.Context, o OrderRequest) (*Preview, error) {
	args, err := r.orderArgs(ctx, o)
	if err != nil {
		return nil, err
	}
	delete(args, "ref_id")
	v, err := r.callJSON(ctx, OpPreviewOrder, args)
	if err != nil {
		return nil, err
	}
	m := payload(v)
	p := &Preview{ApprovalInApp: boolOf(m, "customer_approval_required")}
	p.EstimatedFees, _ = findNum(m, func(k string) bool { return strings.Contains(k, "fee") })
	p.EstimatedCost, _ = findNum(m, func(k string) bool { return k == "estimated_cost" || k == "estimated_total" || k == "total_cost" })
	for _, k := range []string{"order_checks", "alerts", "warnings", "messages"} {
		p.Alerts = append(p.Alerts, alertTexts(m[k])...)
	}
	if p.ApprovalInApp {
		p.Alerts = append(p.Alerts, "Robinhood will ask you to approve this order in its app before it executes.")
	}
	return p, nil
}

func (r *Robinhood) Place(ctx context.Context, o OrderRequest) (*BrokerOrder, error) {
	name := r.tools[OpPlaceOrder]
	if name == "" {
		return nil, errors.New("the Robinhood MCP server has no tool to place orders")
	}
	args, err := r.orderArgs(ctx, o)
	if err != nil {
		return nil, err
	}
	res, err := r.client.CallTool(ctx, name, args)
	if res != nil && res.IsError {
		// The server answered and refused: a definite outcome.
		return nil, fmt.Errorf("%w: %s", ErrRejected, strings.TrimSpace(res.Text()))
	}
	if err != nil {
		return nil, err
	}
	raw, err := res.JSON()
	if err != nil {
		return nil, err
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	d := payload(v)
	om := d
	if inner := asMap(d["order"]); inner != nil {
		om = inner
	}
	bo := orderFrom(om)
	bo.ClientOrderID = o.ClientOrderID
	if boolOf(d, "customer_approval_required") || boolOf(om, "customer_approval_required") {
		bo.Detail = "waiting for your approval in the Robinhood app"
	}
	return bo, nil
}

func (r *Robinhood) Cancel(ctx context.Context, brokerOrderID string) error {
	account, err := r.accountNumber(ctx)
	if err != nil {
		return err
	}
	_, err = r.callJSON(ctx, OpCancelOrder, map[string]any{"account_number": account, "order_id": brokerOrderID})
	return err
}

func (r *Robinhood) Order(ctx context.Context, brokerOrderID string) (*BrokerOrder, error) {
	account, err := r.accountNumber(ctx)
	if err != nil {
		return nil, err
	}
	v, err := r.callJSON(ctx, OpOrderStatus, map[string]any{"account_number": account, "order_id": brokerOrderID})
	if err != nil {
		return nil, err
	}
	orders := listOf(v, "orders")
	if len(orders) == 0 {
		return nil, fmt.Errorf("the broker does not know order %s", brokerOrderID)
	}
	return orderFrom(orders[0]), nil
}

// FindOrder looks for the order a submission whose answer was lost placed.
// Robinhood files a placed order under its approval id rather than the
// ref_id the gateway sent, so it matches the agent-initiated orders since
// the order was prepared by contract, side, quantity and limit price.
func (r *Robinhood) FindOrder(ctx context.Context, o OrderRequest, since time.Time) (*BrokerOrder, error) {
	account, err := r.accountNumber(ctx)
	if err != nil {
		return nil, err
	}
	c, err := r.contractFor(ctx, o.OCCSymbol)
	if err != nil {
		return nil, err
	}
	side := "buy"
	if o.Side == leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_SELL_TO_CLOSE {
		side = "sell"
	}
	v, err := r.callJSON(ctx, OpOrders, map[string]any{
		"account_number": account, "agent_initiated": true, "created_at_gte": since.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	for _, m := range listOf(v, "orders") {
		if uint32(num(m, "quantity")) != o.Quantity || math.Abs(num(m, "price")-o.LimitPrice) > 0.005 {
			continue
		}
		for _, item := range asList(m["legs"]) {
			leg := asMap(item)
			if (str(leg, "option_id") == c.id || strings.Contains(str(leg, "option"), c.id)) && str(leg, "side") == side {
				bo := orderFrom(m)
				bo.ClientOrderID = o.ClientOrderID
				return bo, nil
			}
		}
	}
	return nil, nil
}

func orderFrom(m map[string]any) *BrokerOrder {
	o := &BrokerOrder{
		ID: str(m, "id", "order_id", "approval_id"), ClientOrderID: str(m, "ref_id"),
		FilledQty:    uint32(num(m, "processed_quantity", "filled_quantity")),
		AveragePrice: num(m, "average_price", "price"),
		Detail:       str(m, "reject_reason", "cancel_reason", "detail"),
	}
	switch strings.ToLower(str(m, "state", "status")) {
	case "filled":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_FILLED
	case "partially_filled":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PARTIALLY_FILLED
	case "cancelled", "canceled", "voided":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_CANCELLED
	case "rejected", "failed", "declined":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_REJECTED
	default:
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED
	}
	execs := asList(m["executions"])
	for _, leg := range asList(m["legs"]) {
		execs = append(execs, asList(asMap(leg)["executions"])...)
	}
	for i, e := range execs {
		em := asMap(e)
		key := str(em, "id", "execution_id")
		if key == "" {
			key = strconv.Itoa(i)
		}
		o.Fills = append(o.Fills, BrokerFill{
			Key: key, Quantity: uint32(num(em, "quantity")), Price: num(em, "price"), Fees: num(em, "fees"),
			At: when(em, "timestamp", "settlement_date", "created_at"),
		})
	}
	return o
}

// --- The two views ---

// ReadOnlyBroker is the surface the snapshot and the marks get. It has no
// method that changes anything at the broker.
type ReadOnlyBroker interface {
	Quotes(ctx context.Context, symbols []string) ([]UnderlyingQuote, error)
	OptionChain(ctx context.Context, symbol string, maxDTE int, asOf time.Time) ([]OptionQuote, error)
	OptionQuotes(ctx context.Context, occs []string) ([]OptionQuote, error)
	OptionQuotesByID(ctx context.Context, ids []string) ([]OptionQuote, error)
	Catalysts(ctx context.Context, symbols []string) ([]Catalyst, []string)
	Supports(op string) bool
}

// TradingBroker is the gateway's surface. Only the gateway is given one.
type TradingBroker interface {
	ReadOnlyBroker
	Account(ctx context.Context) (*Account, error)
	Preview(ctx context.Context, o OrderRequest) (*Preview, error)
	Place(ctx context.Context, o OrderRequest) (*BrokerOrder, error)
	Cancel(ctx context.Context, brokerOrderID string) error
	Order(ctx context.Context, brokerOrderID string) (*BrokerOrder, error)
	FindOrder(ctx context.Context, o OrderRequest, since time.Time) (*BrokerOrder, error)
}

// Reader returns the read-only view. The value it returns holds the session
// but exposes only the read methods.
func (r *Robinhood) Reader() ReadOnlyBroker { return readOnly{r} }

// Trader returns the gateway's view.
func (r *Robinhood) Trader() TradingBroker { return r }

type readOnly struct{ r *Robinhood }

func (v readOnly) Quotes(ctx context.Context, s []string) ([]UnderlyingQuote, error) {
	return v.r.Quotes(ctx, s)
}
func (v readOnly) OptionChain(ctx context.Context, s string, d int, t time.Time) ([]OptionQuote, error) {
	return v.r.OptionChain(ctx, s, d, t)
}
func (v readOnly) OptionQuotes(ctx context.Context, o []string) ([]OptionQuote, error) {
	return v.r.OptionQuotes(ctx, o)
}
func (v readOnly) OptionQuotesByID(ctx context.Context, ids []string) ([]OptionQuote, error) {
	return v.r.OptionQuotesByID(ctx, ids)
}
func (v readOnly) Catalysts(ctx context.Context, s []string) ([]Catalyst, []string) {
	return v.r.Catalysts(ctx, s)
}
func (v readOnly) Supports(op string) bool { return slices.Contains(readOps, op) && v.r.Supports(op) }
