package tradinglab

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeBroker is a Robinhood-like MCP server over streamable HTTP. It holds an
// account, quotes and orders, and counts every placement, which is what the
// idempotency tests assert on.
type fakeBroker struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	token        string
	optionsLevel string
	buyingPower  float64
	quoteTime    time.Time
	options      map[string]OptionQuote
	underlyings  map[string]UnderlyingQuote
	orders       map[string]map[string]any
	placed       int
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
		"get_account", "get_quotes", "get_option_chain", "get_option_quotes", "get_orders", "get_option_order",
		"preview_option_order", "place_option_order", "cancel_option_order", "get_events",
	}
	return append(base, b.extraTools...)
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
			tools = append(tools, map[string]any{"name": name, "inputSchema": map[string]any{"type": "object"}})
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
		text, _ := json.Marshal(data)
		reply(map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}})
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": -32601, "message": "no method"}})
	}
}

var errDropReply = fmt.Errorf("drop")

func optionJSON(o OptionQuote, at time.Time) map[string]any {
	return map[string]any{
		"occ_symbol": o.OCCSymbol, "chain_symbol": o.Underlying, "expiration_date": o.Expiration, "strike_price": fmt.Sprint(o.Strike), "type": o.Right,
		"bid_price": fmt.Sprint(o.Bid), "ask_price": fmt.Sprint(o.Ask), "implied_volatility": o.IV, "delta": o.Delta, "open_interest": o.OpenInterest,
		"volume": o.Volume, "updated_at": at.Format(time.RFC3339), "tradability": "tradable",
	}
}

func (b *fakeBroker) call(name string, args map[string]any) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch name {
	case "get_account":
		return map[string]any{"results": []any{map[string]any{"account_number": "5QR12345", "option_buying_power": fmt.Sprint(b.buyingPower), "option_level": b.optionsLevel}}}, nil
	case "get_quotes":
		var out []any
		for _, s := range args["symbols"].([]any) {
			if u, ok := b.underlyings[s.(string)]; ok {
				out = append(out, map[string]any{"symbol": u.Symbol, "last_trade_price": fmt.Sprint(u.Last), "bid_price": u.Bid, "ask_price": u.Ask, "updated_at": b.quoteTime.Format(time.RFC3339)})
			}
		}
		return out, nil
	case "get_option_chain":
		var out []any
		for _, o := range b.options {
			if o.Underlying == args["symbol"] {
				out = append(out, optionJSON(o, b.quoteTime))
			}
		}
		return map[string]any{"results": out}, nil
	case "get_option_quotes":
		var out []any
		for _, s := range args["occ_symbols"].([]any) {
			if o, ok := b.options[s.(string)]; ok {
				out = append(out, optionJSON(o, b.quoteTime))
			}
		}
		return out, nil
	case "get_events":
		return []any{map[string]any{"type": "earnings", "date": b.quoteTime.AddDate(0, 0, 3).Format(time.DateOnly), "title": "Earnings call"}}, nil
	case "preview_option_order":
		return map[string]any{"estimated_fees": "0.06", "alerts": []any{"Options can expire worthless."}}, nil
	case "place_option_order":
		if b.rejectPlace != "" {
			return nil, fmt.Errorf("%s", b.rejectPlace)
		}
		b.placed++
		id := fmt.Sprintf("rh-order-%d", b.placed)
		order := map[string]any{"id": id, "ref_id": args["ref_id"], "state": "queued", "quantity": args["quantity"], "price": args["price"], "occ_symbol": args["occ_symbol"], "side": args["side"]}
		b.orders[id] = order
		if b.dropPlaceReply {
			return nil, errDropReply
		}
		return order, nil
	case "get_orders":
		var out []any
		for _, o := range b.orders {
			if args["ref_id"] == nil || o["ref_id"] == args["ref_id"] {
				out = append(out, o)
			}
		}
		return out, nil
	case "get_option_order":
		if o, ok := b.orders[args["order_id"].(string)]; ok {
			return o, nil
		}
		return nil, fmt.Errorf("no order %v", args["order_id"])
	case "cancel_option_order":
		if o, ok := b.orders[args["order_id"].(string)]; ok {
			o["state"] = "cancelled"
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("no order")
	}
	return nil, fmt.Errorf("unknown tool %s", name)
}

// fill records an execution on the broker's side.
func (b *fakeBroker) fill(orderID string, qty int, price float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.orders[orderID]
	execs, _ := o["executions"].([]any)
	execs = append(execs, map[string]any{"id": fmt.Sprintf("%s-x%d", orderID, len(execs)+1), "quantity": fmt.Sprint(qty), "price": fmt.Sprint(price), "fees": "0.03"})
	o["executions"] = execs
	total := 0
	for _, e := range execs {
		var n int
		_, _ = fmt.Sscan(e.(map[string]any)["quantity"].(string), &n)
		total += n
	}
	o["processed_quantity"] = fmt.Sprint(total)
	want := 0
	switch q := o["quantity"].(type) {
	case float64:
		want = int(q)
	case json.Number:
		n, _ := q.Int64()
		want = int(n)
	}
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
