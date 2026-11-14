package tradinglab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
var readOps = []string{OpQuote, OpOptionChain, OpOptionQuotes, OpNews, OpEvents, OpAccount, OpPositions, OpOrders, OpOrderStatus}

// tradeOps are reachable only through TradingBroker, which only the gateway
// holds.
var tradeOps = []string{OpPreviewOrder, OpPlaceOrder, OpCancelOrder}

// requiredForResearch and requiredForLive are what each mode cannot do without.
var (
	requiredForResearch = []string{OpQuote, OpOptionChain}
	requiredForLive     = []string{OpAccount, OpOptionQuotes, OpPreviewOrder, OpPlaceOrder, OpCancelOrder, OpOrderStatus}
)

var defaultToolNames = map[string][]string{
	OpQuote:        {"get_quotes", "get_stock_quotes", "stock_quotes", "get_quote", "quotes"},
	OpOptionChain:  {"get_option_chain", "get_options_chain", "option_chain", "find_options", "list_option_instruments"},
	OpOptionQuotes: {"get_option_quotes", "get_option_market_data", "option_market_data", "option_quotes"},
	OpNews:         {"get_news", "news", "get_stock_news"},
	OpEvents:       {"get_events", "get_earnings", "earnings", "corporate_events"},
	OpAccount:      {"get_account", "get_accounts", "account", "get_account_profile"},
	OpPositions:    {"get_positions", "get_option_positions", "positions"},
	OpOrders:       {"get_orders", "get_option_orders", "list_orders"},
	OpOrderStatus:  {"get_option_order", "get_order", "order_status"},
	OpPreviewOrder: {"preview_option_order", "preview_order", "review_option_order"},
	OpPlaceOrder:   {"place_option_order", "place_order", "submit_option_order"},
	OpCancelOrder:  {"cancel_option_order", "cancel_order"},
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

// --- Tolerant decoding: servers name fields differently. ---

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// asList unwraps the common envelopes: a bare array, {"results": [...]},
// {"data": [...]}, {"items": [...]}, or a single object.
func asList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case map[string]any:
		for _, k := range []string{"results", "data", "items", "quotes", "options", "orders", "positions", "events", "news"} {
			if inner, ok := t[k]; ok {
				return asList(inner)
			}
		}
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

// --- Read operations ---

// Account is what the gateway needs to know about the brokerage account.
type Account struct {
	ID           string
	BuyingPower  float64
	OptionsLevel string
}

func (r *Robinhood) Account(ctx context.Context) (*Account, error) {
	v, err := r.callJSON(ctx, OpAccount, map[string]any{})
	if err != nil {
		return nil, err
	}
	var chosen map[string]any
	for _, item := range asList(v) {
		m := asMap(item)
		id := str(m, "account_number", "account_id", "id")
		if r.accountID == "" || id == r.accountID {
			chosen = m
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("account %s is not among the server's accounts", maskAccount(r.accountID))
	}
	a := &Account{
		ID:           str(chosen, "account_number", "account_id", "id"),
		BuyingPower:  num(chosen, "option_buying_power", "buying_power", "cash_available_for_options"),
		OptionsLevel: str(chosen, "option_level", "options_level", "option_trading_level"),
	}
	if a.ID == "" {
		return nil, errors.New("the account read returned no account id")
	}
	return a, nil
}

func (r *Robinhood) Quotes(ctx context.Context, symbols []string) ([]UnderlyingQuote, error) {
	v, err := r.callJSON(ctx, OpQuote, map[string]any{"symbols": symbols})
	if err != nil {
		return nil, err
	}
	var out []UnderlyingQuote
	for _, item := range asList(v) {
		m := asMap(item)
		sym := strings.ToUpper(str(m, "symbol", "ticker"))
		if sym == "" {
			continue
		}
		out = append(out, UnderlyingQuote{
			Symbol: sym, Last: num(m, "last_trade_price", "last", "price", "last_extended_hours_trade_price"),
			Bid: num(m, "bid_price", "bid"), Ask: num(m, "ask_price", "ask"), PrevClose: num(m, "previous_close", "prev_close"),
			Volume: int64(num(m, "volume")), QuoteTime: when(m, "updated_at", "quote_time", "timestamp"),
		})
	}
	return out, nil
}

func optionFrom(m map[string]any) (OptionQuote, bool) {
	q := OptionQuote{
		OCCSymbol: str(m, "occ_symbol", "option_symbol", "symbol"), Underlying: strings.ToUpper(str(m, "chain_symbol", "underlying", "underlying_symbol")),
		Expiration: str(m, "expiration_date", "expiration"), Strike: num(m, "strike_price", "strike"), Right: strings.ToLower(str(m, "type", "right", "option_type")),
		Bid: num(m, "bid_price", "bid"), Ask: num(m, "ask_price", "ask"), Last: num(m, "last_trade_price", "last"),
		IV: num(m, "implied_volatility", "iv"), Delta: num(m, "delta"), Gamma: num(m, "gamma"), Theta: num(m, "theta"), Vega: num(m, "vega"),
		OpenInterest: int64(num(m, "open_interest")), Volume: int64(num(m, "volume")), Multiplier: int(num(m, "multiplier", "trade_value_multiplier")),
		QuoteTime: when(m, "updated_at", "quote_time", "timestamp"), Tradable: true,
	}
	if t := str(m, "tradability", "state"); t != "" && t != "tradable" && t != "active" {
		q.Tradable = false
	}
	if occ, err := NormalizeOCC(q.OCCSymbol); err == nil {
		q.OCCSymbol = occ
	} else if occ, err := OCCSymbol(q.Underlying, q.Expiration, q.Right, q.Strike); err == nil {
		q.OCCSymbol = occ
	} else {
		return q, false
	}
	if p, err := ParseOCC(q.OCCSymbol); err == nil {
		q.Expiration, q.Right, q.Strike = p.Expiration, p.Right, p.Strike
		if q.Underlying == "" {
			q.Underlying = p.Root
		}
	}
	return q, true
}

func (r *Robinhood) OptionChain(ctx context.Context, symbol string, maxDTE int, asOf time.Time) ([]OptionQuote, error) {
	args := map[string]any{"symbol": symbol}
	if maxDTE > 0 {
		args["expiration_date_max"] = asOf.AddDate(0, 0, maxDTE).Format(time.DateOnly)
	}
	v, err := r.callJSON(ctx, OpOptionChain, args)
	if err != nil {
		return nil, err
	}
	var out []OptionQuote
	for _, item := range asList(v) {
		if q, ok := optionFrom(asMap(item)); ok {
			out = append(out, q)
		}
	}
	return out, nil
}

func (r *Robinhood) OptionQuotes(ctx context.Context, occs []string) ([]OptionQuote, error) {
	v, err := r.callJSON(ctx, OpOptionQuotes, map[string]any{"occ_symbols": occs})
	if err != nil {
		return nil, err
	}
	var out []OptionQuote
	for _, item := range asList(v) {
		if q, ok := optionFrom(asMap(item)); ok {
			out = append(out, q)
		}
	}
	return out, nil
}

// Catalysts gathers news and corporate events where the server offers them.
func (r *Robinhood) Catalysts(ctx context.Context, symbols []string) ([]Catalyst, []string) {
	var out []Catalyst
	var warnings []string
	for _, op := range []string{OpEvents, OpNews} {
		if !r.Supports(op) {
			warnings = append(warnings, "the broker's MCP server offers no "+op)
			continue
		}
		for _, sym := range symbols {
			v, err := r.callJSON(ctx, op, map[string]any{"symbol": sym})
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s for %s failed: %v", op, sym, err))
				continue
			}
			for _, item := range asList(v) {
				m := asMap(item)
				c := Catalyst{
					Symbol: sym, Type: str(m, "type", "event_type", "category"), Date: str(m, "date", "event_date", "report_date"),
					Title: str(m, "title", "headline", "summary", "name"), Source: str(m, "source", "provider"), URL: str(m, "url", "link"),
					PublishedAt: when(m, "published_at", "published", "created_at"),
				}
				if c.Type == "" {
					c.Type = "news"
					if op == OpEvents {
						c.Type = "corporate_event"
					}
				}
				if len(c.Date) > 10 {
					c.Date = c.Date[:10]
				}
				if c.Title != "" {
					out = append(out, c)
				}
			}
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

func (o OrderRequest) args() map[string]any {
	side, effect := "buy", "open"
	if o.Side == leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_SELL_TO_CLOSE {
		side, effect = "sell", "close"
	}
	args := map[string]any{
		"account_id": o.AccountID, "occ_symbol": o.OCCSymbol, "side": side, "position_effect": effect,
		"quantity": o.Quantity, "type": "limit", "price": strconv.FormatFloat(o.LimitPrice, 'f', 2, 64),
		"time_in_force": o.TimeInForce,
	}
	if o.ClientOrderID != "" {
		args["ref_id"] = o.ClientOrderID
	}
	return args
}

// Preview is the broker's review of an order before it is placed.
type Preview struct {
	EstimatedFees float64
	EstimatedCost float64
	Alerts        []string
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

func (r *Robinhood) Preview(ctx context.Context, o OrderRequest) (*Preview, error) {
	v, err := r.callJSON(ctx, OpPreviewOrder, o.args())
	if err != nil {
		return nil, err
	}
	m := asMap(v)
	p := &Preview{EstimatedFees: num(m, "estimated_fees", "fees", "regulatory_fees"), EstimatedCost: num(m, "estimated_cost", "estimated_total", "total_cost")}
	for _, a := range asList(m["alerts"]) {
		switch t := a.(type) {
		case string:
			p.Alerts = append(p.Alerts, t)
		case map[string]any:
			if s := str(t, "message", "text", "title"); s != "" {
				p.Alerts = append(p.Alerts, s)
			}
		}
	}
	for _, k := range []string{"warnings", "messages"} {
		for _, a := range asList(m[k]) {
			if s, ok := a.(string); ok {
				p.Alerts = append(p.Alerts, s)
			}
		}
	}
	return p, nil
}

func (r *Robinhood) Place(ctx context.Context, o OrderRequest) (*BrokerOrder, error) {
	name := r.tools[OpPlaceOrder]
	if name == "" {
		return nil, errors.New("the Robinhood MCP server has no tool to place orders")
	}
	res, err := r.client.CallTool(ctx, name, o.args())
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
	return orderFrom(asMap(v)), nil
}

func (r *Robinhood) Cancel(ctx context.Context, brokerOrderID string) error {
	_, err := r.callJSON(ctx, OpCancelOrder, map[string]any{"order_id": brokerOrderID})
	return err
}

func (r *Robinhood) Order(ctx context.Context, brokerOrderID string) (*BrokerOrder, error) {
	v, err := r.callJSON(ctx, OpOrderStatus, map[string]any{"order_id": brokerOrderID})
	if err != nil {
		return nil, err
	}
	return orderFrom(asMap(v)), nil
}

// FindOrder looks an order up by the idempotency key the gateway sent, to
// learn the outcome of a submission whose answer was lost.
func (r *Robinhood) FindOrder(ctx context.Context, clientOrderID string) (*BrokerOrder, error) {
	v, err := r.callJSON(ctx, OpOrders, map[string]any{"ref_id": clientOrderID})
	if err != nil {
		return nil, err
	}
	for _, item := range asList(v) {
		o := orderFrom(asMap(item))
		if o.ClientOrderID == clientOrderID {
			return o, nil
		}
	}
	return nil, nil
}

func orderFrom(m map[string]any) *BrokerOrder {
	o := &BrokerOrder{
		ID: str(m, "id", "order_id"), ClientOrderID: str(m, "ref_id", "client_order_id"),
		FilledQty: uint32(num(m, "processed_quantity", "filled_quantity", "cumulative_quantity")), AveragePrice: num(m, "average_price", "avg_fill_price", "price"),
		Detail: str(m, "reject_reason", "cancel_reason", "detail"),
	}
	switch strings.ToLower(str(m, "state", "status")) {
	case "filled":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_FILLED
	case "partially_filled":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PARTIALLY_FILLED
	case "cancelled", "canceled":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_CANCELLED
	case "rejected", "failed":
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_REJECTED
	default:
		o.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED
	}
	for i, e := range asList(m["executions"]) {
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
	FindOrder(ctx context.Context, clientOrderID string) (*BrokerOrder, error)
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
func (v readOnly) Catalysts(ctx context.Context, s []string) ([]Catalyst, []string) {
	return v.r.Catalysts(ctx, s)
}
func (v readOnly) Supports(op string) bool { return slices.Contains(readOps, op) && v.r.Supports(op) }
