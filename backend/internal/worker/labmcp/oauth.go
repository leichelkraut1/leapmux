package labmcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/util/id"
)

// scope is the one scope the connector grants.
const scope = "lab.read"

// hostedCallbacks are the hosted apps' OAuth callbacks: Claude's, and the
// one ChatGPT uses with a server that returns iss (RFC 9207). A loopback
// redirect serves Claude Code, Claude Desktop and Codex.
var hostedCallbacks = []string{
	"https://claude.ai/api/mcp/auth_callback",
	"https://claude.com/api/mcp/auth_callback",
	"https://chatgpt.com/connector_platform_oauth_redirect",
}

// chatgptCallback is ChatGPT's per-connector callback, which it uses instead
// for some connectors.
var chatgptCallback = regexp.MustCompile(`^https://chatgpt\.com/connector/oauth/[A-Za-z0-9_-]{1,128}$`)

func allowedRedirect(raw string) bool {
	if slices.Contains(hostedCallbacks, raw) || chatgptCallback.MatchString(raw) {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// --- Metadata (RFC 9728, RFC 8414) ---

func (s *Server) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.resource(),
		"authorization_servers":    []string{s.resource()},
		"scopes_supported":         []string{scope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "LeapMux trading lab (read-only)",
	})
}

func (s *Server) handleServerMetadata(w http.ResponseWriter, _ *http.Request) {
	r := s.resource()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                r,
		"authorization_endpoint":                r + "/authorize",
		"token_endpoint":                        r + "/token",
		"registration_endpoint":                 r + "/register",
		"scopes_supported":                      []string{scope},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		// Every redirect carries iss, which ChatGPT requires before it uses
		// its stable callback.
		"authorization_response_iss_parameter_supported": true,
	})
}

// --- Dynamic client registration (RFC 7591) ---

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is not JSON client metadata")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 5 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give one to five redirect URIs")
		return
	}
	for _, u := range req.RedirectURIs {
		if !allowedRedirect(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "only Claude's or ChatGPT's callback, or a loopback address, may receive codes")
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "Unnamed client"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	c := &client{ID: id.Generate(), Name: name, RedirectURIs: req.RedirectURIs}
	now := s.now()
	if err := s.store.addClient(r.Context(), c, now.UnixMilli()); err != nil {
		if errors.Is(err, errTooManyClients) {
			oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", err.Error())
			return
		}
		s.log.Error("register client", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register the client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        now.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      scope,
	})
}

// --- Authorization ---

type authRequest struct {
	ClientID, RedirectURI, State, Challenge, Resource string
	ClientName, RedirectHost                          string
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Trading lab sign-in</title>
<style>
:root{color-scheme:light dark;--bg:#f7f7f5;--fg:#1d1d1b;--muted:#5f5f5a;--card:#fff;--line:#d9d9d4;--accent:#2d5bd7;--bad:#b3261e}
@media (prefers-color-scheme:dark){:root{--bg:#161615;--fg:#ececea;--muted:#a3a39d;--card:#20201f;--line:#3a3a37;--accent:#7d9cf0;--bad:#f2867c}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
main{max-width:26rem;margin:10vh auto;padding:0 16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:24px}
h1{font-size:1.25rem;margin:0 0 8px}p{margin:0 0 12px;color:var(--muted)}b{color:var(--fg)}
label{display:block;font-weight:600;margin:16px 0 6px}
input[type=password]{box-sizing:border-box;width:100%;padding:10px 12px;font:inherit;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--fg)}
button{margin-top:16px;width:100%;padding:10px;font:inherit;font-weight:600;border:0;border-radius:8px;background:var(--accent);color:#fff;cursor:pointer}
.err{color:var(--bad);font-weight:600}
</style></head><body><main><div class="card">
{{if .Fatal}}<h1>Can't sign in</h1><p class="err">{{.Fatal}}</p>{{else}}
<h1>Sign in to the trading lab</h1>
<p><b>{{.Req.ClientName}}</b> wants read-only access to your LeapMux trading lab: runs, predictions, shadow portfolios and metrics. It cannot prepare, confirm or cancel an order. After sign-in you go back to <b>{{.Req.RedirectHost}}</b>.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" autocomplete="off">
<input type="hidden" name="client_id" value="{{.Req.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.Req.RedirectURI}}">
<input type="hidden" name="state" value="{{.Req.State}}">
<input type="hidden" name="code_challenge" value="{{.Req.Challenge}}">
<input type="hidden" name="code_challenge_method" value="S256">
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="resource" value="{{.Req.Resource}}">
<label for="pass">Connector passphrase</label>
<input id="pass" name="passphrase" type="password" required autofocus>
<button type="submit">Allow read-only access</button>
</form>{{end}}
</div></main></body></html>`))

func (s *Server) page(w http.ResponseWriter, status int, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://claude.ai https://claude.com https://chatgpt.com http://localhost:* http://127.0.0.1:*; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, data)
}

// parseAuth validates an authorization request. A bad client or redirect
// URI is fatal (never redirected to); the rest go back to the client.
func (s *Server) parseAuth(r *http.Request) (*authRequest, string, string) {
	v := r.Form
	c, err := s.store.getClient(r.Context(), v.Get("client_id"))
	if err != nil {
		return nil, "This sign-in link names an unknown app. Remove the connector in Claude and add it again.", ""
	}
	redirect := v.Get("redirect_uri")
	if redirect == "" && len(c.RedirectURIs) == 1 {
		redirect = c.RedirectURIs[0]
	}
	if !slices.Contains(c.RedirectURIs, redirect) {
		return nil, "This sign-in link names a return address the app did not register.", ""
	}
	u, _ := url.Parse(redirect)
	req := &authRequest{
		ClientID: c.ID, RedirectURI: redirect, State: v.Get("state"), Challenge: v.Get("code_challenge"),
		Resource: v.Get("resource"), ClientName: c.Name, RedirectHost: u.Host,
	}
	switch {
	case v.Get("response_type") != "code":
		return req, "", "unsupported_response_type"
	case v.Get("code_challenge_method") != "S256" || len(req.Challenge) < 43 || len(req.Challenge) > 128:
		return req, "", "invalid_request"
	case req.Resource != "" && strings.TrimRight(req.Resource, "/") != s.resource():
		return req, "", "invalid_target"
	}
	return req, "", ""
}

func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, req *authRequest, params url.Values) {
	if req.State != "" {
		params.Set("state", req.State)
	}
	params.Set("iss", s.resource())
	sep := "?"
	if strings.Contains(req.RedirectURI, "?") {
		sep = "&"
	}
	http.Redirect(w, r, req.RedirectURI+sep+params.Encode(), http.StatusFound)
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.page(w, http.StatusBadRequest, map[string]any{"Fatal": "The sign-in request is malformed."})
		return
	}
	req, fatal, oerr := s.parseAuth(r)
	if fatal != "" {
		s.page(w, http.StatusBadRequest, map[string]any{"Fatal": fatal})
		return
	}
	if oerr != "" {
		s.redirectBack(w, r, req, url.Values{"error": {oerr}})
		return
	}
	owner := s.owner()
	if owner == "" {
		s.page(w, http.StatusServiceUnavailable, map[string]any{"Fatal": "The worker has not learned its owner yet. Try again in a minute."})
		return
	}
	if r.Method == http.MethodGet {
		s.page(w, http.StatusOK, map[string]any{"Req": req})
		return
	}
	if s.locked() {
		s.page(w, http.StatusTooManyRequests, map[string]any{"Req": req, "Error": "Too many wrong passphrases. Sign-in is locked for 15 minutes."})
		return
	}
	s.passMu.Lock()
	err := s.cfg.Lab.CheckConnectorPassphrase(r.Context(), owner, r.PostForm.Get("passphrase"))
	s.passMu.Unlock()
	if err != nil {
		s.recordFailure()
		s.log.Warn("trading connector sign-in refused", "client", req.ClientName)
		s.page(w, http.StatusUnauthorized, map[string]any{"Req": req, "Error": "That passphrase is wrong, or no connector passphrase is set in the lab settings."})
		return
	}
	code := newSecret()
	now := s.now()
	t := &token{Kind: kindCode, ClientID: req.ClientID, UserID: owner, RedirectURI: req.RedirectURI, CodeChallenge: req.Challenge,
		ExpiresAtMs: now.Add(codeTTL).UnixMilli()}
	if err := s.store.putToken(r.Context(), s.store.db, code, t, now.UnixMilli()); err != nil {
		s.log.Error("store authorization code", "error", err)
		s.redirectBack(w, r, req, url.Values{"error": {"server_error"}})
		return
	}
	s.log.Info("trading connector sign-in", "client", req.ClientName)
	s.redirectBack(w, r, req, url.Values{"code": {code}})
}

// --- Token ---

var errGrant = errors.New("invalid grant")

func pkceMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body is not a form")
		return
	}
	f := r.PostForm
	clientID := f.Get("client_id")
	var secret string
	var kind tokenKind
	var check func(*token) error
	switch f.Get("grant_type") {
	case "authorization_code":
		secret, kind = f.Get("code"), kindCode
		check = func(t *token) error {
			if t.ClientID != clientID || t.RedirectURI != f.Get("redirect_uri") || !pkceMatches(f.Get("code_verifier"), t.CodeChallenge) {
				return errGrant
			}
			return nil
		}
	case "refresh_token":
		secret, kind = f.Get("refresh_token"), kindRefresh
		check = func(t *token) error {
			if t.ClientID != clientID {
				return errGrant
			}
			return nil
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	if secret == "" || clientID == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "missing the code or token, or the client id")
		return
	}
	access, refresh := newSecret(), newSecret()
	t, err := s.store.redeem(r.Context(), secret, kind, s.now().UnixMilli(), check, access, refresh,
		accessTTL.Milliseconds(), refreshTTL.Milliseconds())
	switch {
	case errors.Is(err, errNotFound) || errors.Is(err, errGrant):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code or token is invalid, expired or already used")
		return
	case err != nil:
		s.log.Error("redeem token", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue a token")
		return
	}
	if t.UserID != s.owner() {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the grant belongs to another user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         scope,
	})
}
