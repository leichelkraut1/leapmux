package tradinglab

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBroker is Robinhood's agent MCP server over streamable HTTP, answering
// with the envelopes and field names the live server uses ({"data": ...},
// results[].quote, chains, instruments by UUID, accounts with
// agentic_allowed). It holds quotes and orders, and counts every placement,
// which is what the idempotency tests assert on.
type fakeBroker struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	token        string
	optionsLevel string
	buyingPower  float64
	quoteTime    time.Time
	options      map[string]OptionQuote // by OCC symbol
	underlyings  map[string]UnderlyingQuote
	orders       map[string]map[string]any
	placed       int
	calls        []string
	// dropPlaceReply makes a placement reach the broker but its answer get
	// lost, as a dropped connection would.
	dropPlaceReply bool
	rejectPlace    string
	extraTools     []string
}

func newFakeBroker(t *testing.T, f *Frame) *fakeBroker {
	b := &fakeBroker{
		t: t, token: "good-token", optionsLevel: "option_level_2", buyingPower: 5000, quoteTime: f.AsOf,
		options: map[string]OptionQuote{}, underlyings: map[string]UnderlyingQuote{}, orders: map[string]map[string]any{},
	}
	for _, o := range f.Options {
		occ, _ := NormalizeOCC(o.OCCSymbol)
		o.OCCSymbol = occ
		b.options[occ] = o
	}
	for _, u := range f.Underlyings {
		b.underlyings[u.Symbol] = u
	}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

// instrumentID is the fake's UUID for a contract.
func instrumentID(occ string) string { return "inst-" + strings.ReplaceAll(occ, " ", "_") }

// set changes the broker's state under its lock, since its handler runs on
// the server's goroutines.
func (b *fakeBroker) set(fn func(b *fakeBroker)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(b)
}

func (b *fakeBroker) tools() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	base := []string{
		"get_accounts", "get_portfolio", "get_equity_quotes", "get_option_chains", "get_option_instruments", "get_option_quotes",
		"get_earnings_results", "get_option_orders", "review_option_order", "place_option_order", "cancel_option_order",
		"place_equity_order", "add_to_watchlist", "get_bank_accounts",
	}
	return append(base, b.extraTools...)
}

// toolCalls lists the tools called so far, in order.
func (b *fakeBroker) toolCalls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

func (b *fakeBroker) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	reply := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
	}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "session-1")
		reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "fake-robinhood", "version": "1"}, "capabilities": map[string]any{}})
	case "tools/list":
		var tools []map[string]any
		for _, name := range b.tools() {
			tools = append(tools, map[string]any{"name": name, "description": "the " + name + " tool", "inputSchema": map[string]any{"type": "object"}})
		}
		reply(map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		data, err := b.call(p.Name, p.Arguments)
		if err == errDropReply {
			// The broker acted; the connection died before the answer.
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
		}
		if err != nil {
			reply(map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}})
			return
		}
		text, _ := json.Marshal(map[string]any{"data": data, "guide": "fake"})
		reply(map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}})
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": -32601, "message": "no method"}})
	}
}

var errDropReply = fmt.Errorf("drop")

func instrumentJSON(o OptionQuote) map[string]any {
	return map[string]any{
		"id": instrumentID(o.OCCSymbol), "chain_symbol": o.Underlying, "expiration_date": o.Expiration,
		"strike_price": fmt.Sprintf("%.4f", o.Strike), "type": o.Right, "state": "active", "tradability": "tradable",
		"trade_value_multiplier": "100.0000",
	}
}

func optionQuoteJSON(o OptionQuote, at time.Time) map[string]any {
	return map[string]any{"quote": map[string]any{
		"instrument_id": instrumentID(o.OCCSymbol), "bid_price": fmt.Sprint(o.Bid), "ask_price": fmt.Sprint(o.Ask),
		"mark_price": fmt.Sprint((o.Bid + o.Ask) / 2), "implied_volatility": fmt.Sprint(o.IV), "delta": fmt.Sprint(o.Delta),
		"open_interest": o.OpenInterest, "volume": o.Volume, "updated_at": at.Format(time.RFC3339Nano),
	}}
}

func csv(v any) []string {
	s, _ := v.(string)
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func (b *fakeBroker) call(name string, args map[string]any) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, name)
	switch name {
	case "get_accounts":
		return map[string]any{"accounts": []any{
			map[string]any{"account_number": "9ZZ00000", "agentic_allowed": false, "option_level": "option_level_3", "state": "active"},
			map[string]any{"account_number": "5QR12345", "agentic_allowed": true, "option_level": b.optionsLevel, "state": "active"},
		}}, nil
	case "get_portfolio":
		if args["account_number"] != "5QR12345" {
			return nil, fmt.Errorf("unknown account %v", args["account_number"])
		}
		return map[string]any{"buying_power": map[string]any{"amount": fmt.Sprint(b.buyingPower), "currency": "USD"}}, nil
	case "get_equity_quotes":
		var out []any
		for _, s := range strs(args["symbols"]) {
			if u, ok := b.underlyings[s]; ok {
				out = append(out, map[string]any{"quote": map[string]any{
					"symbol": u.Symbol, "last_trade_price": fmt.Sprint(u.Last), "bid_price": fmt.Sprint(u.Bid), "ask_price": fmt.Sprint(u.Ask),
					"previous_close": fmt.Sprint(u.PrevClose), "venue_bid_time": b.quoteTime.Format(time.RFC3339Nano), "state": "active",
				}, "close": map[string]any{"symbol": u.Symbol}})
			}
		}
		return map[string]any{"results": out}, nil
	case "get_option_chains":
		sym, _ := args["underlying_symbol"].(string)
		var exps []string
		for _, o := range b.options {
			if o.Underlying == sym && !slices.Contains(exps, o.Expiration) {
				exps = append(exps, o.Expiration)
			}
		}
		slices.Sort(exps)
		if len(exps) == 0 {
			return map[string]any{"chains": []any{}}, nil
		}
		return map[string]any{"chains": []any{map[string]any{"id": "chain-" + sym, "symbol": sym, "can_open_position": true, "expiration_dates": exps}}}, nil
	case "get_option_instruments":
		ids := csv(args["ids"])
		var rows []any
		for occ, o := range b.options {
			switch {
			case len(ids) > 0:
				if !slices.Contains(ids, instrumentID(occ)) {
					continue
				}
			case o.Underlying != args["chain_symbol"]:
				continue
			case args["expiration_dates"] != nil && !slices.Contains(csv(args["expiration_dates"]), o.Expiration):
				continue
			case args["type"] != nil && args["type"] != o.Right:
				continue
			case args["strike_price"] != nil && args["strike_price"] != fmt.Sprintf("%.4f", o.Strike):
				continue
			}
			rows = append(rows, instrumentJSON(o))
		}
		return map[string]any{"instruments": rows, "next": nil}, nil
	case "get_option_quotes":
		var out []any
		for _, id := range strs(args["instrument_ids"]) {
			for occ, o := range b.options {
				if instrumentID(occ) == id {
					out = append(out, optionQuoteJSON(o, b.quoteTime))
				}
			}
		}
		return map[string]any{"results": out}, nil
	case "get_earnings_results":
		return map[string]any{"results": []any{map[string]any{
			"symbol": args["symbol"], "year": "2026", "quarter": "4",
			"report": map[string]any{"date": b.quoteTime.AddDate(0, 0, 3).Format(time.DateOnly), "timing": "pm"},
		}}}, nil
	case "review_option_order":
		return map[string]any{"estimated_fees": "0.06", "order_checks": []any{map[string]any{"message": "Options can expire worthless."}}}, nil
	case "place_option_order":
		if b.rejectPlace != "" {
			return nil, fmt.Errorf("%s", b.rejectPlace)
		}
		if args["account_number"] != "5QR12345" {
			return nil, fmt.Errorf("account %v is not agentic", args["account_number"])
		}
		b.placed++
		id := fmt.Sprintf("rh-order-%d", b.placed)
		legs := args["legs"].([]any)
		leg := legs[0].(map[string]any)
		order := map[string]any{
			"id": id, "ref_id": "approval-" + id, "state": "queued", "quantity": args["quantity"], "price": args["price"],
			"created_at": b.quoteTime.Format(time.RFC3339Nano),
			"legs":       []any{map[string]any{"option_id": leg["option_id"], "side": leg["side"], "position_effect": leg["position_effect"]}},
		}
		b.orders[id] = order
		if b.dropPlaceReply {
			return nil, errDropReply
		}
		return order, nil
	case "get_option_orders":
		var out []any
		for id, o := range b.orders {
			if args["order_id"] == nil || args["order_id"] == id {
				out = append(out, o)
			}
		}
		return map[string]any{"orders": out}, nil
	case "cancel_option_order":
		if o, ok := b.orders[args["order_id"].(string)]; ok {
			o["state"] = "cancelled"
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("no order")
	}
	return nil, fmt.Errorf("unknown tool %s", name)
}

// fill records an execution on the broker's side, on the order's leg as
// Robinhood reports it.
func (b *fakeBroker) fill(orderID string, qty int, price float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.orders[orderID]
	leg := o["legs"].([]any)[0].(map[string]any)
	execs, _ := leg["executions"].([]any)
	execs = append(execs, map[string]any{"id": fmt.Sprintf("%s-x%d", orderID, len(execs)+1), "quantity": fmt.Sprint(qty), "price": fmt.Sprint(price), "fees": "0.03"})
	leg["executions"] = execs
	total := 0
	for _, e := range execs {
		var n int
		_, _ = fmt.Sscan(e.(map[string]any)["quantity"].(string), &n)
		total += n
	}
	o["processed_quantity"] = fmt.Sprint(total)
	var want int
	_, _ = fmt.Sscan(fmt.Sprint(o["quantity"]), &want)
	o["state"] = "partially_filled"
	if total >= want {
		o["state"] = "filled"
	}
}

func (b *fakeBroker) placements() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.placed
}

func (b *fakeBroker) url() string { return b.srv.URL + "/mcp" }

// orderIDs lists the broker's order ids.
func (b *fakeBroker) orderIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for id := range b.orders {
		out = append(out, id)
	}
	return out
}
