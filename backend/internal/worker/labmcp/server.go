// Package labmcp is the trading lab's read-only Claude connector: an MCP
// endpoint (streamable HTTP, JSON responses) and the OAuth 2.1 server that
// signs a person in to it, so Claude on a phone can read the lab.
//
// The worker serves it only when trading_connector_listen is set, on that
// local address; a reverse proxy publishes it at trading_connector_url. It
// answers for the worker's owner alone, with the connector passphrase set in
// the lab settings. Its tools only read: none prepares, confirms or cancels
// an order, decides a candidate or reaches the broker, and none returns a
// broker credential, an account id or an order fingerprint.
package labmcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/tradinglab"
)

const (
	codeTTL    = 5 * time.Minute
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	// A run of wrong passphrases locks sign-in for a while: the endpoint is
	// public, and it has one user, so the lock is global.
	maxFailures   = 5
	failureWindow = 15 * time.Minute
	maxBody       = 64 << 10
)

// RunLister lists the user's workflow runs (*workflow.Engine).
type RunLister interface {
	ListRuns(ctx context.Context, userID string) ([]*leapmuxv1.WorkflowRunSummary, error)
}

// Config configures a Server.
type Config struct {
	DB   *sql.DB
	Lab  *tradinglab.Lab
	Runs RunLister
	// Owner returns the worker owner's user id; empty refuses everyone.
	Owner func() string
	// PublicURL is the MCP endpoint as Claude reaches it, such as
	// https://leapmux.example.com/lab-mcp. Its path is the server's base.
	PublicURL string
	Version   string
	Clock     quartz.Clock
	Logger    *slog.Logger
}

// Server is the connector.
type Server struct {
	cfg    Config
	store  *store
	clock  quartz.Clock
	log    *slog.Logger
	public *url.URL
	base   string

	// passMu serializes passphrase checks: argon2id takes 64 MiB each.
	passMu   sync.Mutex
	failMu   sync.Mutex
	failures []time.Time
}

// New builds a Server.
func New(cfg Config) (*Server, error) {
	u, err := url.Parse(strings.TrimRight(cfg.PublicURL, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("trading connector URL %q must be an https URL with no query", cfg.PublicURL)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	clock := cfg.Clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg: cfg, store: &store{db: cfg.DB}, clock: clock, log: log.With("component", "labmcp"),
		public: u, base: strings.TrimRight(u.Path, "/"),
	}, nil
}

// Serve listens on addr until ctx ends.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	s.log.Info("trading connector listening", "addr", ln.Addr().String(), "url", s.resource())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) now() time.Time { return s.clock.Now().UTC() }

func (s *Server) origin() string { return s.public.Scheme + "://" + s.public.Host }

// resource is the MCP endpoint's URL, which is also the OAuth issuer.
func (s *Server) resource() string { return s.origin() + s.base }

func (s *Server) resourceMetadataURL() string {
	return s.origin() + "/.well-known/oauth-protected-resource" + s.base
}

// Handler routes the MCP endpoint, the OAuth endpoints and their metadata,
// at the paths the public URL puts them.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	endpoint := s.base
	if endpoint == "" {
		endpoint = "/"
	}
	mux.HandleFunc(endpoint, s.handleMCP)
	for _, p := range []string{"/.well-known/oauth-protected-resource" + s.base, s.base + "/.well-known/oauth-protected-resource"} {
		mux.HandleFunc("GET "+p, s.handleResourceMetadata)
	}
	for _, p := range []string{
		"/.well-known/oauth-authorization-server" + s.base, s.base + "/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration" + s.base, s.base + "/.well-known/openid-configuration",
	} {
		mux.HandleFunc("GET "+p, s.handleServerMetadata)
	}
	mux.HandleFunc("POST "+s.base+"/register", s.handleRegister)
	mux.HandleFunc(s.base+"/authorize", s.handleAuthorize)
	mux.HandleFunc("POST "+s.base+"/token", s.handleToken)
	return http.MaxBytesHandler(securityHeaders(mux), maxBody)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// owner is the worker owner, or empty when unknown.
func (s *Server) owner() string {
	if s.cfg.Owner == nil {
		return ""
	}
	return s.cfg.Owner()
}

// locked reports whether too many wrong passphrases came in the window.
func (s *Server) locked() bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	cutoff := s.now().Add(-failureWindow)
	kept := s.failures[:0]
	for _, t := range s.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.failures = kept
	return len(kept) >= maxFailures
}

func (s *Server) recordFailure() {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	s.failures = append(s.failures, s.now())
}
