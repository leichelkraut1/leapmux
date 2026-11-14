package labmcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/tradinglab"
)

const (
	owner    = "user-owner"
	connPass = "phone sign-in passphrase"
	callback = "https://claude.ai/api/mcp/auth_callback"
	verifier = "a-verifier-that-is-long-enough-for-pkce-0123456789abcdef"
)

type fakeRuns struct {
	runs []*leapmuxv1.WorkflowRunSummary
}

func (f *fakeRuns) ListRuns(context.Context, string) ([]*leapmuxv1.WorkflowRunSummary, error) {
	return f.runs, nil
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	lab    *tradinglab.Lab
	srv    *Server
	http   *httptest.Server
	client *http.Client
	owner  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := workerdb.Open(filepath.Join(t.TempDir(), "worker.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), db))
	clock := testutil.NewQuartzMock(t)
	lab := tradinglab.New(tradinglab.Config{DB: db, Clock: clock, NewID: func() string { return "id" }})
	_, err = lab.SaveSettings(t.Context(), owner, &leapmuxv1.SaveTradingLabSettingsRequest{
		Settings: tradinglab.DefaultSettings(), NewConnectorPassphrase: connPass,
	})
	require.NoError(t, err)
	h := &harness{t: t, ctx: t.Context(), lab: lab, owner: owner}
	runs := &fakeRuns{runs: []*leapmuxv1.WorkflowRunSummary{
		{Run: &leapmuxv1.WorkflowRun{Id: "run-lab", Name: "Monday scan", Template: &leapmuxv1.WorkflowTemplate{Name: tradinglab.TemplateName()}, CreatedAtMs: 2}},
		{Run: &leapmuxv1.WorkflowRun{Id: "run-other", Name: "A code review", Template: &leapmuxv1.WorkflowTemplate{Name: "Code review"}, CreatedAtMs: 3}},
	}}
	h.srv, err = New(Config{DB: db, Lab: lab, Runs: runs, Owner: func() string { return h.owner }, PublicURL: "https://lab.example.com/lab-mcp", Clock: clock})
	require.NoError(t, err)
	h.http = httptest.NewServer(h.srv.Handler())
	t.Cleanup(h.http.Close)
	h.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return h
}

func (h *harness) do(method, path string, body io.Reader, header map[string]string) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.ctx, method, h.http.URL+path, body)
	require.NoError(h.t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	return resp, b
}

func (h *harness) form(path string, v url.Values) (*http.Response, []byte) {
	return h.do(http.MethodPost, path, strings.NewReader(v.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

func (h *harness) register(redirect string) (int, string) {
	resp, b := h.do(http.MethodPost, "/lab-mcp/register", strings.NewReader(`{"client_name":"Claude","redirect_uris":["`+redirect+`"]}`), nil)
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out.ClientID
}

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (h *harness) authValues(clientID string) url.Values {
	return url.Values{
		"client_id": {clientID}, "redirect_uri": {callback}, "response_type": {"code"}, "state": {"st-1"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "resource": {"https://lab.example.com/lab-mcp"},
	}
}

// signIn runs registration and authorization and returns a code.
func (h *harness) signIn() (clientID, code string) {
	h.t.Helper()
	status, clientID := h.register(callback)
	require.Equal(h.t, http.StatusCreated, status)
	v := h.authValues(clientID)
	v.Set("passphrase", connPass)
	resp, _ := h.form("/lab-mcp/authorize", v)
	require.Equal(h.t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(h.t, err)
	require.Equal(h.t, callback, loc.Scheme+"://"+loc.Host+loc.Path)
	assert.Equal(h.t, "st-1", loc.Query().Get("state"))
	assert.Equal(h.t, "https://lab.example.com/lab-mcp", loc.Query().Get("iss"))
	return clientID, loc.Query().Get("code")
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Error   string `json:"error"`
}

func (h *harness) exchange(v url.Values) (int, tokens) {
	resp, b := h.form("/lab-mcp/token", v)
	var out tokens
	require.NoError(h.t, json.Unmarshal(b, &out))
	return resp.StatusCode, out
}

func (h *harness) tokensFor(clientID, code string) tokens {
	h.t.Helper()
	status, tk := h.exchange(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {callback}, "code_verifier": {verifier}})
	require.Equal(h.t, http.StatusOK, status, tk.Error)
	return tk
}

// rpc posts one JSON-RPC request and returns the response's result.
func (h *harness) rpc(access, method string, params any) (int, map[string]any) {
	h.t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(h.t, err)
	resp, b := h.do(http.MethodPost, "/lab-mcp", strings.NewReader(string(body)), map[string]string{
		"Authorization": "Bearer " + access, "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
	})
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var out struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(h.t, json.Unmarshal(b, &out))
	return resp.StatusCode, out.Result
}

func (h *harness) call(access, name string, args map[string]any) (bool, map[string]any, string) {
	h.t.Helper()
	status, res := h.rpc(access, "tools/call", map[string]any{"name": name, "arguments": args})
	require.Equal(h.t, http.StatusOK, status)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := res["isError"].(bool)
	var out map[string]any
	if !isErr {
		require.NoError(h.t, json.Unmarshal([]byte(text), &out))
	}
	return isErr, out, text
}

func TestServerPublishesDiscoveryMetadata(t *testing.T) {
	h := newHarness(t)
	resp, b := h.do(http.MethodPost, "/lab-mcp", strings.NewReader(`{}`), nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), `resource_metadata="https://lab.example.com/.well-known/oauth-protected-resource/lab-mcp"`)
	assert.NotContains(t, string(b), "lab_overview")

	_, b = h.do(http.MethodGet, "/.well-known/oauth-protected-resource/lab-mcp", nil, nil)
	var prm map[string]any
	require.NoError(t, json.Unmarshal(b, &prm))
	assert.Equal(t, "https://lab.example.com/lab-mcp", prm["resource"])
	assert.Equal(t, []any{"https://lab.example.com/lab-mcp"}, prm["authorization_servers"])

	for _, p := range []string{"/.well-known/oauth-authorization-server/lab-mcp", "/lab-mcp/.well-known/oauth-authorization-server"} {
		_, b = h.do(http.MethodGet, p, nil, nil)
		var as map[string]any
		require.NoError(t, json.Unmarshal(b, &as), p)
		assert.Equal(t, "https://lab.example.com/lab-mcp/token", as["token_endpoint"], p)
		assert.Equal(t, []any{"S256"}, as["code_challenge_methods_supported"], p)
		assert.Equal(t, true, as["authorization_response_iss_parameter_supported"], p)
	}
}

func TestServerRegistersOnlyClaudeAndLoopbackRedirects(t *testing.T) {
	h := newHarness(t)
	for _, ok := range []string{
		callback, "https://claude.com/api/mcp/auth_callback", "http://localhost:33418/callback", "http://127.0.0.1:5000/cb",
		"https://chatgpt.com/connector_platform_oauth_redirect", "https://chatgpt.com/connector/oauth/abc_123-XYZ",
	} {
		status, id := h.register(ok)
		assert.Equal(t, http.StatusCreated, status, ok)
		assert.NotEmpty(t, id, ok)
	}
	for _, bad := range []string{"https://evil.example.com/cb", "https://claude.ai/elsewhere", "http://claude.ai/api/mcp/auth_callback", "http://192.168.1.5/cb", "javascript:alert(1)",
		"https://chatgpt.com/elsewhere", "https://chatgpt.com/connector/oauth/a/b", "https://chatgpt.com/connector/oauth/x?next=https://evil.example.com",
		"https://chatgpt.com.evil.example.com/connector_platform_oauth_redirect"} {
		status, _ := h.register(bad)
		assert.Equal(t, http.StatusBadRequest, status, bad)
	}
}

func TestServerSignsInExchangesAndRefreshesTokens(t *testing.T) {
	h := newHarness(t)
	clientID, code := h.signIn()

	// A wrong verifier or redirect fails without spending the code.
	status, tk := h.exchange(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {callback}, "code_verifier": {verifier + "x"}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", tk.Error)
	status, _ = h.exchange(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {"http://localhost:1/cb"}, "code_verifier": {verifier}})
	assert.Equal(t, http.StatusBadRequest, status)

	first := h.tokensFor(clientID, code)
	require.NotEmpty(t, first.Access)
	require.NotEmpty(t, first.Refresh)

	// A code works once.
	status, _ = h.exchange(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {callback}, "code_verifier": {verifier}})
	assert.Equal(t, http.StatusBadRequest, status)

	status, res := h.rpc(first.Access, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "2025-06-18", res["protocolVersion"])

	// Refresh rotates: the old refresh token is spent.
	status, second := h.exchange(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.Refresh}, "client_id": {clientID}})
	require.Equal(t, http.StatusOK, status)
	assert.NotEqual(t, first.Access, second.Access)
	status, _ = h.exchange(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.Refresh}, "client_id": {clientID}})
	assert.Equal(t, http.StatusBadRequest, status)
	status, _ = h.rpc(second.Access, "ping", nil)
	assert.Equal(t, http.StatusOK, status)
}

func TestServerRefusesWrongPassphrasesAndLocksAfterFive(t *testing.T) {
	h := newHarness(t)
	status, clientID := h.register(callback)
	require.Equal(t, http.StatusCreated, status)

	resp, b := h.do(http.MethodGet, "/lab-mcp/authorize?"+h.authValues(clientID).Encode(), nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(b), "Connector passphrase")
	assert.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))

	for _, wrong := range []string{"guess 0", "guess 1", "guess 2", "guess 3", "guess 4"} {
		v := h.authValues(clientID)
		v.Set("passphrase", wrong)
		resp, _ := h.form("/lab-mcp/authorize", v)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, wrong)
		assert.Empty(t, resp.Header.Get("Location"))
	}
	v := h.authValues(clientID)
	v.Set("passphrase", connPass)
	resp, b = h.form("/lab-mcp/authorize", v)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Contains(t, string(b), "locked")
}

func TestServerNeverRedirectsToAnUnregisteredAddress(t *testing.T) {
	h := newHarness(t)
	_, clientID := h.register(callback)
	v := h.authValues(clientID)
	v.Set("redirect_uri", "https://evil.example.com/cb")
	v.Set("passphrase", connPass)
	resp, _ := h.form("/lab-mcp/authorize", v)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))

	v = h.authValues("no-such-client")
	resp, _ = h.do(http.MethodGet, "/lab-mcp/authorize?"+v.Encode(), nil, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// A plain challenge goes back to the client as an error, with no code.
	v = h.authValues(clientID)
	v.Set("code_challenge_method", "plain")
	resp, _ = h.do(http.MethodGet, "/lab-mcp/authorize?"+v.Encode(), nil, nil)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	assert.Equal(t, "invalid_request", loc.Query().Get("error"))
	assert.Empty(t, loc.Query().Get("code"))
}

func TestServerToolsOnlyRead(t *testing.T) {
	h := newHarness(t)
	tk := h.tokensFor(h.signIn())

	status, res := h.rpc(tk.Access, "tools/list", nil)
	require.Equal(t, http.StatusOK, status)
	var names []string
	for _, raw := range res["tools"].([]any) {
		tool := raw.(map[string]any)
		names = append(names, tool["name"].(string))
		assert.Equal(t, true, tool["annotations"].(map[string]any)["readOnlyHint"], tool["name"])
	}
	assert.Equal(t, []string{"get_run", "lab_overview", "list_orders", "list_positions", "list_runs"}, names)

	isErr, out, _ := h.call(tk.Access, "lab_overview", nil)
	require.False(t, isErr)
	assert.Equal(t, "paper", out["mode"])
	assert.Len(t, out["books"], 4)
	assert.NotContains(t, out, "robinhood")

	isErr, out, _ = h.call(tk.Access, "list_runs", nil)
	require.False(t, isErr)
	runs := out["runs"].([]any)
	require.Len(t, runs, 1, "only trading runs are listed")
	assert.Equal(t, "run-lab", runs[0].(map[string]any)["id"])

	isErr, out, _ = h.call(tk.Access, "get_run", map[string]any{"run_id": "run-lab"})
	require.False(t, isErr)
	assert.Equal(t, "run-lab", out["lab"].(map[string]any)["run_id"])
	isErr, _, text := h.call(tk.Access, "get_run", map[string]any{"run_id": "run-other"})
	assert.True(t, isErr)
	assert.Equal(t, "not found", text)

	isErr, _, text = h.call(tk.Access, "list_positions", map[string]any{"book": "everything"})
	assert.True(t, isErr)
	assert.Contains(t, text, "book must be one of")

	status, _ = h.rpc(tk.Access, "tools/call", map[string]any{"name": "confirm_order", "arguments": map[string]any{}})
	assert.Equal(t, http.StatusOK, status)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "confirm_order"}})
	_, b := h.do(http.MethodPost, "/lab-mcp", strings.NewReader(string(body)), map[string]string{"Authorization": "Bearer " + tk.Access})
	assert.Contains(t, string(b), "unknown tool")
}

func TestServerRedactsOrderAccountAndFingerprint(t *testing.T) {
	o := redactOrder(&leapmuxv1.TradingOrder{Id: "o1", AccountId: "acct-123", Fingerprint: "fp", OccSymbol: "SPY"})
	assert.Equal(t, "o1", o.GetId())
	assert.Empty(t, o.GetAccountId())
	assert.Empty(t, o.GetFingerprint())
}

func TestServerRefusesSignedOutTokensAndOtherOwners(t *testing.T) {
	h := newHarness(t)
	tk := h.tokensFor(h.signIn())
	status, _ := h.rpc(tk.Access, "ping", nil)
	require.Equal(t, http.StatusOK, status)

	h.owner = "someone-else"
	status, _ = h.rpc(tk.Access, "ping", nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	h.owner = owner

	_, err := h.lab.SaveSettings(h.ctx, owner, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: tradinglab.DefaultSettings(), SignOutConnector: true})
	require.NoError(t, err)
	status, _ = h.rpc(tk.Access, "ping", nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = h.exchange(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}, "client_id": {"x"}})
	assert.Equal(t, http.StatusBadRequest, status)
}

func TestServerRefusesAForeignOrigin(t *testing.T) {
	h := newHarness(t)
	tk := h.tokensFor(h.signIn())
	resp, _ := h.do(http.MethodPost, "/lab-mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), map[string]string{
		"Authorization": "Bearer " + tk.Access, "Origin": "https://evil.example.com",
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
