package labmcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/tradinglab"
)

// protocolVersions are the MCP revisions the endpoint speaks, newest first.
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const instructions = `Read-only view of the user's LeapMux blind trading research lab: options research runs with frozen, hashed predictions from isolated blind and contrarian analysts, an evaluator's anonymized scores, a risk audit, and three shadow portfolios (blind, contrarian, blind-inverse) filled at bid/ask, plus any live book. Start with lab_overview. Times are Unix milliseconds. Nothing here can place, confirm or cancel an order: the user does that in LeapMux, confirming each exact order with a passphrase.`

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) unauthorized(w http.ResponseWriter, invalid bool) {
	v := fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, s.resourceMetadataURL(), scope)
	if invalid {
		v += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", v)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && o != s.origin() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
		return
	}
	authz := r.Header.Get("Authorization")
	secret, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok || secret == "" {
		s.unauthorized(w, false)
		return
	}
	t, err := s.store.lookup(r.Context(), secret, kindAccess, s.now().UnixMilli())
	owner := s.owner()
	if err != nil || owner == "" || t.UserID != owner {
		if err != nil && !errors.Is(err, errNotFound) {
			s.log.Error("look up access token", "error", err)
		}
		s.unauthorized(w, true)
		return
	}
	if r.Method != http.MethodPost {
		// No server-initiated stream and no session to end.
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "expected one JSON-RPC 2.0 request"}})
		return
	}
	if len(req.ID) == 0 {
		// A notification: nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.dispatch(r.Context(), owner, &req)
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatch(ctx context.Context, owner string, req *rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "leapmux-trading-lab", "title": "LeapMux trading lab", "version": s.cfg.Version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolList}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		tool, ok := tools[p.Name]
		if !ok {
			return nil, &rpcError{-32602, "unknown tool " + p.Name}
		}
		var args map[string]any
		if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
			if err := json.Unmarshal(p.Arguments, &args); err != nil {
				return toolError("arguments must be a JSON object"), nil
			}
		}
		out, err := tool.run(s, ctx, owner, args)
		if err != nil {
			if errors.Is(err, tradinglab.ErrNotFound) {
				return toolError("not found"), nil
			}
			var bad badArg
			if errors.As(err, &bad) {
				return toolError(string(bad)), nil
			}
			s.log.Error("trading connector tool", "tool", p.Name, "error", err)
			return toolError("the lab could not answer: " + err.Error()), nil
		}
		text, err := json.Marshal(out)
		if err != nil {
			return nil, &rpcError{-32603, "could not encode the result"}
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func toolError(msg string) map[string]any {
	return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": msg}}}
}

type badArg string

func (b badArg) Error() string { return string(b) }

// --- Tools ---

type tool struct {
	def map[string]any
	run func(s *Server, ctx context.Context, owner string, args map[string]any) (any, error)
}

func readOnlyTool(name, title, desc string, props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name": name, "title": title, "description": desc, "inputSchema": schema,
		"annotations": map[string]any{"title": title, "readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
}

var bookWords = map[string]leapmuxv1.TradingBook{
	"blind": leapmuxv1.TradingBook_TRADING_BOOK_BLIND, "contrarian": leapmuxv1.TradingBook_TRADING_BOOK_CONTRARIAN,
	"blind_inverse": leapmuxv1.TradingBook_TRADING_BOOK_BLIND_INVERSE, "live": leapmuxv1.TradingBook_TRADING_BOOK_LIVE,
}

var tools = map[string]tool{
	"lab_overview": {
		def: readOnlyTool("lab_overview", "Trading lab overview",
			"The lab's mode (paper or live), market data source, watchlist, risk budget and simulation rules; metrics for each book (net P&L, return on risk, win rate, drawdown, Brier score, 3x/5x hit rates, P&L at each horizon); the approved trade queue; open positions; the last broker check; active execution policies; and the 10 latest orders.",
			map[string]any{}),
		run: (*Server).overview,
	},
	"list_runs": {
		def: readOnlyTool("list_runs", "List research runs",
			"Trading research runs, newest first: id, name, objective, status, task progress and timestamps. Pass a run id to get_run for its predictions.",
			map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "How many runs (default 20)."}}),
		run: (*Server).listRuns,
	},
	"get_run": {
		def: readOnlyTool("get_run", "Get a research run",
			"One run: its frozen market snapshot (hash, as-of time, symbols), the blind and contrarian prediction hashes, the evaluator's and risk auditor's verdicts, which anonymous label was whose (only once recorded), and every candidate trade with its probabilities, entry limit, max loss, thesis and status.",
			map[string]any{"run_id": map[string]any{"type": "string", "description": "A run id from list_runs."}}, "run_id"),
		run: (*Server).getRun,
	},
	"list_positions": {
		def: readOnlyTool("list_positions", "List positions",
			"Positions in the shadow books (blind, contrarian, blind_inverse) and the live book, with entry, cost, fees, last mark, realized P&L, peak multiple and horizon marks.",
			map[string]any{
				"book":  map[string]any{"type": "string", "enum": []string{"all", "blind", "contrarian", "blind_inverse", "live"}, "description": "Default all."},
				"state": map[string]any{"type": "string", "enum": []string{"all", "open", "closed"}, "description": "open, or closed (closed, expired, not filled). Default all."},
			}),
		run: (*Server).listPositions,
	},
	"list_orders": {
		def: readOnlyTool("list_orders", "List orders",
			"Live orders the user prepared or confirmed in LeapMux, newest first: state, side, contract, quantity, limit, fills and broker detail. Read-only: orders are confirmed only in LeapMux.",
			map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "How many orders (default 20)."}}),
		run: (*Server).listOrders,
	},
}

var toolList = func() []map[string]any {
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	slices.Sort(names)
	out := make([]map[string]any, len(names))
	for i, n := range names {
		out[i] = tools[n].def
	}
	return out
}()

var pj = protojson.MarshalOptions{UseProtoNames: true, UseEnumNumbers: false}

// raw renders a proto message as JSON.
func raw(m proto.Message) json.RawMessage {
	b, err := pj.Marshal(m)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

func rawList[T proto.Message](ms []T) []json.RawMessage {
	out := make([]json.RawMessage, len(ms))
	for i, m := range ms {
		out[i] = raw(m)
	}
	return out
}

// redactOrder drops what the connector never shows: the brokerage account
// and the fingerprint an order is confirmed against.
func redactOrder(o *leapmuxv1.TradingOrder) *leapmuxv1.TradingOrder {
	c := proto.Clone(o).(*leapmuxv1.TradingOrder)
	c.AccountId, c.Fingerprint = "", ""
	return c
}

func intArg(args map[string]any, key string, def, maxV int) (int, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	f, ok := v.(float64)
	if !ok || f != float64(int(f)) || f < 1 || int(f) > maxV {
		return 0, badArg(fmt.Sprintf("%s must be a whole number from 1 to %d", key, maxV))
	}
	return int(f), nil
}

func stringArg(args map[string]any, key string, allowed ...string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", nil
	}
	str, ok := v.(string)
	if !ok || (len(allowed) > 0 && !slices.Contains(allowed, str)) {
		return "", badArg(fmt.Sprintf("%s must be one of %s", key, strings.Join(allowed, ", ")))
	}
	return str, nil
}

func (s *Server) overview(ctx context.Context, owner string, _ map[string]any) (any, error) {
	d, err := s.cfg.Lab.Dashboard(ctx, owner)
	if err != nil {
		return nil, err
	}
	set := d.GetSettings()
	var open []*leapmuxv1.TradingPosition
	for _, p := range d.GetPositions() {
		if p.GetState() == leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
			open = append(open, p)
		}
	}
	var policies []*leapmuxv1.TradingExecutionPolicy
	for _, p := range d.GetPolicies() {
		if p.GetActive() {
			policies = append(policies, p)
		}
	}
	orders := newestOrders(d.GetOrders(), 10)
	return map[string]any{
		"mode":                     strings.ToLower(strings.TrimPrefix(set.GetMode().String(), "TRADING_LAB_MODE_")),
		"market_data":              strings.ToLower(strings.TrimPrefix(set.GetMarketData().String(), "TRADING_MARKET_DATA_SOURCE_")),
		"watchlist":                set.GetWatchlist(),
		"max_snapshot_age_seconds": set.GetMaxSnapshotAgeSeconds(),
		"risk_budget":              raw(set.GetRisk()),
		"simulation":               raw(set.GetSimulation()),
		"books":                    rawList(d.GetMetrics()),
		"approved_queue":           rawList(d.GetQueue()),
		"open_positions":           rawList(open),
		"broker":                   raw(d.GetBroker()),
		"active_policies":          rawList(policies),
		"latest_orders":            rawList(orders),
	}, nil
}

func newestOrders(all []*leapmuxv1.TradingOrder, n int) []*leapmuxv1.TradingOrder {
	sorted := slices.Clone(all)
	slices.SortStableFunc(sorted, func(a, b *leapmuxv1.TradingOrder) int {
		return cmp.Compare(b.GetCreatedAtMs(), a.GetCreatedAtMs())
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	out := make([]*leapmuxv1.TradingOrder, len(sorted))
	for i, o := range sorted {
		out[i] = redactOrder(o)
	}
	return out
}

// tradingRuns are the user's runs the lab took a snapshot for, plus any run
// of the bundled template that has not reached its snapshot yet.
func (s *Server) tradingRuns(ctx context.Context, owner string) ([]*leapmuxv1.WorkflowRunSummary, error) {
	ids, err := s.cfg.Lab.RunIDs(ctx, owner)
	if err != nil {
		return nil, err
	}
	all, err := s.cfg.Runs.ListRuns(ctx, owner)
	if err != nil {
		return nil, err
	}
	template := tradinglab.TemplateName()
	var out []*leapmuxv1.WorkflowRunSummary
	for _, r := range all {
		if slices.Contains(ids, r.GetRun().GetId()) || r.GetRun().GetTemplate().GetName() == template {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b *leapmuxv1.WorkflowRunSummary) int {
		return cmp.Compare(b.GetRun().GetCreatedAtMs(), a.GetRun().GetCreatedAtMs())
	})
	return out, nil
}

func runJSON(r *leapmuxv1.WorkflowRunSummary) map[string]any {
	run := r.GetRun()
	return map[string]any{
		"id":                run.GetId(),
		"name":              run.GetName(),
		"objective":         run.GetObjective(),
		"status":            strings.ToLower(strings.TrimPrefix(run.GetStatus().String(), "WORKFLOW_RUN_STATUS_")),
		"status_detail":     run.GetStatusDetail(),
		"summary":           run.GetSummary(),
		"tasks_total":       r.GetTasksTotal(),
		"tasks_succeeded":   r.GetTasksSucceeded(),
		"tasks_active":      r.GetTasksActive(),
		"tasks_blocked":     r.GetTasksBlocked(),
		"approvals_pending": r.GetApprovalsPending(),
		"created_at_ms":     run.GetCreatedAtMs(),
		"completed_at_ms":   run.GetCompletedAtMs(),
	}
}

func (s *Server) listRuns(ctx context.Context, owner string, args map[string]any) (any, error) {
	limit, err := intArg(args, "limit", 20, 100)
	if err != nil {
		return nil, err
	}
	runs, err := s.tradingRuns(ctx, owner)
	if err != nil {
		return nil, err
	}
	if len(runs) > limit {
		runs = runs[:limit]
	}
	out := make([]map[string]any, len(runs))
	for i, r := range runs {
		out[i] = runJSON(r)
	}
	return map[string]any{"runs": out}, nil
}

func (s *Server) getRun(ctx context.Context, owner string, args map[string]any) (any, error) {
	runID, err := stringArg(args, "run_id")
	if err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, badArg("run_id is required")
	}
	runs, err := s.tradingRuns(ctx, owner)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(runs, func(r *leapmuxv1.WorkflowRunSummary) bool { return r.GetRun().GetId() == runID })
	if i < 0 {
		return nil, tradinglab.ErrNotFound
	}
	view, err := s.cfg.Lab.RunView(ctx, owner, runID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run": runJSON(runs[i]), "lab": raw(view)}, nil
}

func (s *Server) listPositions(ctx context.Context, owner string, args map[string]any) (any, error) {
	book, err := stringArg(args, "book", "all", "blind", "contrarian", "blind_inverse", "live")
	if err != nil {
		return nil, err
	}
	state, err := stringArg(args, "state", "all", "open", "closed")
	if err != nil {
		return nil, err
	}
	d, err := s.cfg.Lab.Dashboard(ctx, owner)
	if err != nil {
		return nil, err
	}
	var out []*leapmuxv1.TradingPosition
	for _, p := range d.GetPositions() {
		if b, ok := bookWords[book]; ok && p.GetBook() != b {
			continue
		}
		isOpen := p.GetState() == leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN
		if (state == "open" && !isOpen) || (state == "closed" && isOpen) {
			continue
		}
		out = append(out, p)
	}
	return map[string]any{"positions": rawList(out)}, nil
}

func (s *Server) listOrders(ctx context.Context, owner string, args map[string]any) (any, error) {
	limit, err := intArg(args, "limit", 20, 100)
	if err != nil {
		return nil, err
	}
	d, err := s.cfg.Lab.Dashboard(ctx, owner)
	if err != nil {
		return nil, err
	}
	return map[string]any{"orders": rawList(newestOrders(d.GetOrders(), limit))}, nil
}
