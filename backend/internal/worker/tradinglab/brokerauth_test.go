package tradinglab

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// fakeOAuthBroker is a broker MCP server's sign-in, shaped like Robinhood's:
// resource metadata naming an issuer at the MCP path, authorization server
// metadata at the origin, registration, and a token endpoint.
type fakeOAuthBroker struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	challenge  string
	redirect   string
	refreshes  int
	refreshBad bool
}

func newFakeOAuthBroker(t *testing.T) *fakeOAuthBroker {
	f := &fakeOAuthBroker{t: t}
	mux := http.NewServeMux()
	issuer := func() string { return f.srv.URL + "/mcp/trading" }
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp/trading", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": issuer(), "authorization_servers": []string{issuer()}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer(), "authorization_endpoint": f.srv.URL + "/oauth", "token_endpoint": f.srv.URL + "/token",
			"registration_endpoint": f.srv.URL + "/register", "scopes_supported": []string{"internal"},
			"authorization_response_iss_parameter_supported": true,
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		f.mu.Lock()
		f.redirect = req.RedirectURIs[0]
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "client-1"})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("code") != "code-1" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge ||
				r.PostForm.Get("client_id") != "client-1" || r.PostForm.Get("redirect_uri") != f.redirect {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 600})
		case "refresh_token":
			f.refreshes++
			if f.refreshBad || r.PostForm.Get("refresh_token") != "refresh-1" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "expires_in": 600})
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOAuthBroker) mcpURL() string { return f.srv.URL + "/mcp/trading" }

func signInHarness(t *testing.T) (*labHarness, *fakeOAuthBroker) {
	h := newLabHarness(t)
	f := newFakeOAuthBroker(t)
	h.lab.connectorURL = "https://lab.example.com/lab-mcp"
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	s.Robinhood.Url, s.Robinhood.Auth = f.mcpURL(), leapmuxv1.TradingBrokerAuth_TRADING_BROKER_AUTH_SIGN_IN
	_, err = h.lab.SaveSettings(h.ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	require.NoError(t, err)
	return h, f
}

// start begins a sign-in and returns its authorize URL's query.
func start(t *testing.T, h *labHarness, f *fakeOAuthBroker) url.Values {
	raw, err := h.lab.StartBrokerSignIn(h.ctx, userID)
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, f.srv.URL+"/oauth", u.Scheme+"://"+u.Host+u.Path)
	q := u.Query()
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	return q
}

func TestBrokerSignInRegistersAndAsksForTheConnectorCallback(t *testing.T) {
	h, f := signInHarness(t)
	q := start(t, h, f)
	assert.Equal(t, "client-1", q.Get("client_id"))
	assert.Equal(t, "https://lab.example.com/lab-mcp"+BrokerCallbackPath, q.Get("redirect_uri"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, f.mcpURL(), q.Get("resource"))
	assert.Equal(t, "internal", q.Get("scope"))
	assert.NotEmpty(t, q.Get("state"))

	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	assert.False(t, s.GetBrokerSignIn().GetSignedIn())
}

func TestBrokerSignInStoresSealedTokensAndRefreshes(t *testing.T) {
	h, f := signInHarness(t)
	q := start(t, h, f)
	issuer := f.mcpURL()

	assert.ErrorIs(t, h.lab.CompleteBrokerSignIn(h.ctx, "unknown-state", "code-1", issuer), ErrDenied)
	require.NoError(t, h.lab.CompleteBrokerSignIn(h.ctx, q.Get("state"), "code-1", issuer))
	assert.ErrorIs(t, h.lab.CompleteBrokerSignIn(h.ctx, q.Get("state"), "code-1", issuer), ErrDenied, "a state works once")

	var access, refresh string
	require.NoError(t, h.db.QueryRow(`SELECT access_sealed, refresh_sealed FROM trading_broker_auth`).Scan(&access, &refresh))
	assert.NotContains(t, access, "access-1")
	assert.NotContains(t, refresh, "refresh-1")
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	assert.True(t, s.GetBrokerSignIn().GetSignedIn())
	assert.Equal(t, f.mcpURL(), s.GetBrokerSignIn().GetServerUrl())

	tok, err := h.lab.brokerToken(h.ctx, userID, f.mcpURL())
	require.NoError(t, err)
	assert.Equal(t, "access-1", tok)
	assert.Zero(t, f.refreshes)

	h.clock.Set(h.clock.Now().Add(10 * time.Minute))
	tok, err = h.lab.brokerToken(h.ctx, userID, f.mcpURL())
	require.NoError(t, err)
	assert.Equal(t, "access-2", tok, "refreshed near expiry")
	assert.Equal(t, 1, f.refreshes)

	_, err = h.lab.brokerToken(h.ctx, userID, "https://other.example.com/mcp")
	assert.ErrorContains(t, err, "another broker server")
}

func TestBrokerSignInRefusesAnotherIssuerAndEndsOnARevokedRefresh(t *testing.T) {
	h, f := signInHarness(t)
	q := start(t, h, f)
	assert.ErrorIs(t, h.lab.CompleteBrokerSignIn(h.ctx, q.Get("state"), "code-1", "https://evil.example.com"), ErrDenied)

	q = start(t, h, f)
	assert.ErrorIs(t, h.lab.CompleteBrokerSignIn(h.ctx, q.Get("state"), "code-1", ""), ErrDenied, "the broker promised iss")

	q = start(t, h, f)
	require.NoError(t, h.lab.CompleteBrokerSignIn(h.ctx, q.Get("state"), "code-1", f.mcpURL()))
	f.mu.Lock()
	f.refreshBad = true
	f.mu.Unlock()
	h.clock.Set(h.clock.Now().Add(10 * time.Minute))
	_, err := h.lab.brokerToken(h.ctx, userID, f.mcpURL())
	assert.ErrorContains(t, err, "sign in again")
	s, err := h.lab.Settings(h.ctx, userID)
	require.NoError(t, err)
	assert.False(t, s.GetBrokerSignIn().GetSignedIn())
}

func TestBrokerSignInExpiresAndSignsOut(t *testing.T) {
	h, f := signInHarness(t)
	q := start(t, h, f)
	h.clock.Set(h.clock.Now().Add(brokerSignInTTL + time.Second))
	assert.ErrorIs(t, h.lab.CompleteBrokerSignIn(h.ctx, q.Get("state"), "code-1", f.mcpURL()), ErrDenied)

	q = start(t, h, f)
	require.NoError(t, h.lab.CompleteBrokerSignIn(h.ctx, q.Get("state"), "code-1", f.mcpURL()))
	s, err := h.lab.SignOutBroker(h.ctx, userID)
	require.NoError(t, err)
	assert.False(t, s.GetBrokerSignIn().GetSignedIn())
	_, err = h.lab.brokerToken(h.ctx, userID, f.mcpURL())
	assert.ErrorContains(t, err, "not signed in")
}

func TestBrokerSignInNeedsTheConnectorURL(t *testing.T) {
	h, _ := signInHarness(t)
	h.lab.connectorURL = ""
	_, err := h.lab.StartBrokerSignIn(h.ctx, userID)
	assert.ErrorIs(t, err, ErrConflict)
	assert.True(t, strings.Contains(err.Error(), "trading_connector_url"))
}

func TestSealedSecretsRoundTrip(t *testing.T) {
	sealed, err := sealSecret([]byte("a token"))
	require.NoError(t, err)
	assert.NotContains(t, sealed, "a token")
	got, err := openSecret(sealed)
	require.NoError(t, err)
	assert.Equal(t, "a token", string(got))
	_, err = openSecret("rot13:nope")
	assert.Error(t, err)
}
