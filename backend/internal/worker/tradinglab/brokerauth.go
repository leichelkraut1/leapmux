package tradinglab

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Broker sign-in is OAuth 2.1 with PKCE against the broker's MCP server, as
// its own metadata describes it (RFC 9728 for the resource, RFC 8414 for the
// authorization server, RFC 7591 to register). Robinhood's
// agent.robinhood.com/mcp/trading publishes all three. The worker is a public
// client: the person signs in in their browser, and the server sends them
// back to <trading connector URL>/broker/callback, which labmcp hands to
// CompleteBrokerSignIn. The tokens are sealed for the worker's OS user
// (sealSecret) and never leave the worker; research agents never see them.

const (
	brokerSignInTTL = 10 * time.Minute
	// A token this close to expiry is refreshed before use.
	brokerRefreshMargin = time.Minute
	brokerHTTPTimeout   = 20 * time.Second
)

// BrokerCallbackPath is where the broker sends the browser back, under the
// trading connector URL.
const BrokerCallbackPath = "/broker/callback"

type brokerOAuth struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	Scopes                []string `json:"scopes_supported"`
	IssInResponse         bool     `json:"authorization_response_iss_parameter_supported"`
}

type pendingSignIn struct {
	userID, serverURL, clientID, verifier, redirect string
	meta                                            brokerOAuth
	expires                                         time.Time
}

// brokerSignIns holds sign-ins waiting for the browser, by state, and
// serializes token refreshes.
type brokerSignIns struct {
	mu        sync.Mutex
	pending   map[string]*pendingSignIn
	refreshMu sync.Mutex
}

type brokerAuthRow struct {
	serverURL, redirectURI, clientID, issuer, tokenEndpoint string
	accessSealed, refreshSealed                             string
	accessExpiresAtMs, signedInAtMs                         int64
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// checkEndpoint refuses an endpoint that is not https, except on loopback.
func checkEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("the broker named an unusable URL %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if host := u.Hostname(); u.Scheme == "http" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("the broker named %q, which is not https", raw)
}

var brokerHTTP = &http.Client{Timeout: brokerHTTPTimeout}

func getJSON(ctx context.Context, raw string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := brokerHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", raw, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// postJSONish posts a body and decodes a JSON answer, reporting an OAuth
// error by its code.
func postJSONish(ctx context.Context, raw, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := brokerHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error != "" {
			return &oauthError{code: e.Error, desc: e.Description}
		}
		return fmt.Errorf("%s answered %d", raw, resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

type oauthError struct{ code, desc string }

func (e *oauthError) Error() string {
	if e.desc != "" {
		return e.code + ": " + e.desc
	}
	return e.code
}

// wellKnown lists the RFC 8414/9728 locations for a URL: the path-inserted
// one first, then the origin's.
func wellKnown(raw, name string) []string {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	out := []string{}
	if u.Path != "" && u.Path != "/" {
		out = append(out, origin+"/.well-known/"+name+u.Path)
	}
	return append(out, origin+"/.well-known/"+name)
}

// discoverBroker reads the server's resource and authorization metadata.
func discoverBroker(ctx context.Context, serverURL string) (brokerOAuth, error) {
	var prm struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	var err error
	for _, u := range wellKnown(serverURL, "oauth-protected-resource") {
		if err = getJSON(ctx, u, &prm); err == nil && len(prm.AuthorizationServers) > 0 {
			break
		}
	}
	if len(prm.AuthorizationServers) == 0 {
		return brokerOAuth{}, fmt.Errorf("the server publishes no sign-in metadata: %v", err)
	}
	issuer := prm.AuthorizationServers[0]
	var meta brokerOAuth
	candidates := append(wellKnown(issuer, "oauth-authorization-server"), wellKnown(issuer, "openid-configuration")...)
	for _, u := range candidates {
		meta = brokerOAuth{}
		if err = getJSON(ctx, u, &meta); err == nil && meta.AuthorizationEndpoint != "" && meta.TokenEndpoint != "" {
			break
		}
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return brokerOAuth{}, fmt.Errorf("the sign-in server publishes no endpoints: %v", err)
	}
	if meta.Issuer == "" {
		meta.Issuer = issuer
	}
	for _, e := range []string{meta.AuthorizationEndpoint, meta.TokenEndpoint} {
		if err := checkEndpoint(e); err != nil {
			return brokerOAuth{}, err
		}
	}
	return meta, nil
}

func registerBrokerClient(ctx context.Context, meta brokerOAuth, redirect string) (string, error) {
	if meta.RegistrationEndpoint == "" {
		return "", errors.New("the broker does not let apps register themselves")
	}
	if err := checkEndpoint(meta.RegistrationEndpoint); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"client_name": "LeapMux trading lab", "redirect_uris": []string{redirect},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := postJSONish(ctx, meta.RegistrationEndpoint, "application/json", strings.NewReader(string(body)), &out); err != nil {
		return "", fmt.Errorf("register with the broker: %w", err)
	}
	if out.ClientID == "" {
		return "", errors.New("the broker registered no client id")
	}
	return out.ClientID, nil
}

func getBrokerAuth(ctx context.Context, q querier, userID string) (*brokerAuthRow, error) {
	r := &brokerAuthRow{}
	err := q.QueryRowContext(ctx, `SELECT server_url, redirect_uri, client_id, issuer, token_endpoint, access_sealed, refresh_sealed,
		access_expires_at_ms, signed_in_at_ms FROM trading_broker_auth WHERE user_id = ?`, userID).
		Scan(&r.serverURL, &r.redirectURI, &r.clientID, &r.issuer, &r.tokenEndpoint, &r.accessSealed, &r.refreshSealed, &r.accessExpiresAtMs, &r.signedInAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func putBrokerAuth(ctx context.Context, q querier, userID string, r *brokerAuthRow, nowMs int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO trading_broker_auth (user_id, server_url, redirect_uri, client_id, issuer, token_endpoint,
		access_sealed, refresh_sealed, access_expires_at_ms, signed_in_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET server_url = excluded.server_url, redirect_uri = excluded.redirect_uri,
		client_id = excluded.client_id, issuer = excluded.issuer, token_endpoint = excluded.token_endpoint,
		access_sealed = excluded.access_sealed, refresh_sealed = excluded.refresh_sealed,
		access_expires_at_ms = excluded.access_expires_at_ms, signed_in_at_ms = excluded.signed_in_at_ms, updated_at_ms = excluded.updated_at_ms`,
		userID, r.serverURL, r.redirectURI, r.clientID, r.issuer, r.tokenEndpoint, r.accessSealed, r.refreshSealed,
		r.accessExpiresAtMs, r.signedInAtMs, nowMs)
	return err
}

// brokerSignInStatus is what the settings show of the sign-in.
func brokerSignInStatus(ctx context.Context, q querier, userID string) (*leapmuxv1.TradingBrokerSignIn, error) {
	r, err := getBrokerAuth(ctx, q, userID)
	if errors.Is(err, ErrNotFound) {
		return &leapmuxv1.TradingBrokerSignIn{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.TradingBrokerSignIn{
		SignedIn: r.refreshSealed != "" || r.accessSealed != "", ServerUrl: r.serverURL,
		SignedInAtMs: r.signedInAtMs, AccessExpiresAtMs: r.accessExpiresAtMs,
	}, nil
}

// StartBrokerSignIn returns the page where the person signs in to the
// broker configured in the settings. It registers the worker with the
// broker the first time.
func (l *Lab) StartBrokerSignIn(ctx context.Context, userID string) (string, error) {
	if l.connectorURL == "" {
		return "", conflictf("signing in needs this worker's trading connector URL, where the broker sends you back (trading_connector_url)")
	}
	s, err := getSettings(ctx, l.store.db, userID)
	if err != nil {
		return "", err
	}
	server := strings.TrimSpace(s.GetRobinhood().GetUrl())
	if server == "" {
		return "", invalidf("set the broker's MCP server URL first")
	}
	if err := checkEndpoint(server); err != nil {
		return "", invalidf("%v", err)
	}
	meta, err := discoverBroker(ctx, server)
	if err != nil {
		return "", conflictf("%v", err)
	}
	redirect := strings.TrimRight(l.connectorURL, "/") + BrokerCallbackPath
	row, err := getBrokerAuth(ctx, l.store.db, userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	clientID := ""
	if row != nil && row.serverURL == server && row.redirectURI == redirect && row.issuer == meta.Issuer {
		clientID = row.clientID
	}
	if clientID == "" {
		if clientID, err = registerBrokerClient(ctx, meta, redirect); err != nil {
			return "", conflictf("%v", err)
		}
		// A new client starts signed out; a sign-in to another server ends.
		if err := putBrokerAuth(ctx, l.store.db, userID, &brokerAuthRow{
			serverURL: server, redirectURI: redirect, clientID: clientID, issuer: meta.Issuer, tokenEndpoint: meta.TokenEndpoint,
		}, l.now().UnixMilli()); err != nil {
			return "", err
		}
	}
	verifier, state := randomToken(), randomToken()
	sum := sha256.Sum256([]byte(verifier))
	l.signIns.mu.Lock()
	if l.signIns.pending == nil {
		l.signIns.pending = map[string]*pendingSignIn{}
	}
	now := l.now()
	for k, p := range l.signIns.pending {
		if now.After(p.expires) {
			delete(l.signIns.pending, k)
		}
	}
	l.signIns.pending[state] = &pendingSignIn{userID: userID, serverURL: server, clientID: clientID, verifier: verifier,
		redirect: redirect, meta: meta, expires: now.Add(brokerSignInTTL)}
	l.signIns.mu.Unlock()

	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {server},
	}
	if len(meta.Scopes) > 0 {
		q.Set("scope", strings.Join(meta.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return meta.AuthorizationEndpoint + sep + q.Encode(), nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// storeTokens seals and stores a token response onto the row.
func (l *Lab) storeTokens(ctx context.Context, userID string, row *brokerAuthRow, t *tokenResponse, signedIn bool) error {
	if t.AccessToken == "" {
		return errors.New("the broker returned no access token")
	}
	now := l.now()
	access, err := sealSecret([]byte(t.AccessToken))
	if err != nil {
		return err
	}
	row.accessSealed = access
	if t.RefreshToken != "" {
		if row.refreshSealed, err = sealSecret([]byte(t.RefreshToken)); err != nil {
			return err
		}
	}
	expiresIn := time.Duration(t.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Hour
	}
	row.accessExpiresAtMs = now.Add(expiresIn).UnixMilli()
	if signedIn {
		row.signedInAtMs = now.UnixMilli()
	}
	return putBrokerAuth(ctx, l.store.db, userID, row, now.UnixMilli())
}

// CompleteBrokerSignIn finishes a sign-in the broker sent back with a code.
// state is single-use and expires; iss, when the broker sends it, must be
// the issuer the sign-in started with (RFC 9207).
func (l *Lab) CompleteBrokerSignIn(ctx context.Context, state, code, iss string) error {
	l.signIns.mu.Lock()
	p := l.signIns.pending[state]
	delete(l.signIns.pending, state)
	l.signIns.mu.Unlock()
	if p == nil || l.now().After(p.expires) {
		return deniedf("this sign-in link is unknown or expired; start again from the lab settings")
	}
	if iss != "" && iss != p.meta.Issuer || iss == "" && p.meta.IssInResponse {
		return deniedf("the sign-in came back from an unexpected issuer")
	}
	if code == "" {
		return deniedf("the broker sent no authorization code")
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.redirect},
		"client_id": {p.clientID}, "code_verifier": {p.verifier}, "resource": {p.serverURL},
	}
	var t tokenResponse
	if err := postJSONish(ctx, p.meta.TokenEndpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &t); err != nil {
		return conflictf("the broker refused the sign-in: %v", err)
	}
	row := &brokerAuthRow{serverURL: p.serverURL, redirectURI: p.redirect, clientID: p.clientID, issuer: p.meta.Issuer, tokenEndpoint: p.meta.TokenEndpoint}
	if err := l.storeTokens(ctx, p.userID, row, &t, true); err != nil {
		return err
	}
	l.log.Info("signed in to the broker", "server", p.serverURL)
	return nil
}

// brokerToken returns a current access token for the user's sign-in to
// server, refreshing it when it nears expiry.
func (l *Lab) brokerToken(ctx context.Context, userID, server string) (string, error) {
	l.signIns.refreshMu.Lock()
	defer l.signIns.refreshMu.Unlock()
	row, err := getBrokerAuth(ctx, l.store.db, userID)
	if errors.Is(err, ErrNotFound) || err == nil && row.accessSealed == "" && row.refreshSealed == "" {
		return "", errors.New("not signed in to the broker; sign in from the lab settings")
	}
	if err != nil {
		return "", err
	}
	if row.serverURL != strings.TrimSpace(server) {
		return "", errors.New("the sign-in is for another broker server; sign in again from the lab settings")
	}
	if row.accessSealed != "" && l.now().Add(brokerRefreshMargin).UnixMilli() < row.accessExpiresAtMs {
		tok, err := openSecret(row.accessSealed)
		return string(tok), err
	}
	if row.refreshSealed == "" {
		return "", errors.New("the broker sign-in expired; sign in again from the lab settings")
	}
	refresh, err := openSecret(row.refreshSealed)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {string(refresh)}, "client_id": {row.clientID}, "resource": {row.serverURL}}
	var t tokenResponse
	if err := postJSONish(ctx, row.tokenEndpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &t); err != nil {
		var oe *oauthError
		if errors.As(err, &oe) && oe.code == "invalid_grant" {
			row.accessSealed, row.refreshSealed, row.accessExpiresAtMs = "", "", 0
			_ = putBrokerAuth(ctx, l.store.db, userID, row, l.now().UnixMilli())
			return "", errors.New("the broker ended the sign-in; sign in again from the lab settings")
		}
		return "", fmt.Errorf("refresh the broker sign-in: %w", err)
	}
	if err := l.storeTokens(ctx, userID, row, &t, false); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// SignOutBroker forgets the user's broker tokens; the registration stays.
func (l *Lab) SignOutBroker(ctx context.Context, userID string) (*leapmuxv1.TradingLabSettings, error) {
	if _, err := l.store.db.ExecContext(ctx, `UPDATE trading_broker_auth SET access_sealed = '', refresh_sealed = '',
		access_expires_at_ms = 0, signed_in_at_ms = 0, updated_at_ms = ? WHERE user_id = ?`, l.now().UnixMilli(), userID); err != nil {
		return nil, err
	}
	return l.Settings(ctx, userID)
}
