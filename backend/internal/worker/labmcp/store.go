package labmcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// tokenKind is what a trading_connector_tokens row holds.
type tokenKind leapmuxv1.TradingConnectorTokenKind

const (
	kindCode    = tokenKind(leapmuxv1.TradingConnectorTokenKind_TRADING_CONNECTOR_TOKEN_KIND_CODE)
	kindAccess  = tokenKind(leapmuxv1.TradingConnectorTokenKind_TRADING_CONNECTOR_TOKEN_KIND_ACCESS)
	kindRefresh = tokenKind(leapmuxv1.TradingConnectorTokenKind_TRADING_CONNECTOR_TOKEN_KIND_REFRESH)
)

// maxClients bounds the dynamically registered clients, since anyone can
// register one; a registration first drops clients that never got a token.
const maxClients = 100

var errNotFound = errors.New("not found")

type client struct {
	ID           string
	Name         string
	RedirectURIs []string
}

type token struct {
	Kind          tokenKind
	ClientID      string
	UserID        string
	RedirectURI   string
	CodeChallenge string
	ExpiresAtMs   int64
}

// newSecret is 32 random bytes, URL-safe.
func newSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type store struct{ db *sql.DB }

func (s *store) addClient(ctx context.Context, c *client, nowMs int64) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A client nobody finished signing in with in a day is abandoned.
	if _, err := tx.ExecContext(ctx, `DELETE FROM trading_connector_clients WHERE created_at_ms < ?
		AND id NOT IN (SELECT client_id FROM trading_connector_tokens)`, nowMs-24*3600*1000); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM trading_connector_clients`).Scan(&n); err != nil {
		return err
	}
	if n >= maxClients {
		return errTooManyClients
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO trading_connector_clients (id, name, redirect_uris, created_at_ms) VALUES (?, ?, ?, ?)`,
		c.ID, c.Name, string(uris), nowMs); err != nil {
		return err
	}
	return tx.Commit()
}

var errTooManyClients = errors.New("too many registered clients")

func (s *store) getClient(ctx context.Context, id string) (*client, error) {
	c := &client{ID: id}
	var uris string
	err := s.db.QueryRowContext(ctx, `SELECT name, redirect_uris FROM trading_connector_clients WHERE id = ?`, id).Scan(&c.Name, &uris)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, json.Unmarshal([]byte(uris), &c.RedirectURIs)
}

func (s *store) putToken(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, secret string, t *token, nowMs int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO trading_connector_tokens
		(hash, token_kind, client_id, user_id, redirect_uri, code_challenge, expires_at_ms, created_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hashSecret(secret), t.Kind, t.ClientID, t.UserID, t.RedirectURI, t.CodeChallenge, t.ExpiresAtMs, nowMs)
	return err
}

// lookup finds an unexpired secret of a kind.
func (s *store) lookup(ctx context.Context, secret string, kind tokenKind, nowMs int64) (*token, error) {
	t := &token{Kind: kind}
	err := s.db.QueryRowContext(ctx, `SELECT client_id, user_id, redirect_uri, code_challenge, expires_at_ms FROM trading_connector_tokens
		WHERE hash = ? AND token_kind = ? AND expires_at_ms > ?`, hashSecret(secret), kind, nowMs).
		Scan(&t.ClientID, &t.UserID, &t.RedirectURI, &t.CodeChallenge, &t.ExpiresAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return t, err
}

// redeem takes a code or refresh token, once: it deletes the secret and
// issues an access and a refresh token in the same transaction, so two
// concurrent redemptions cannot both succeed.
func (s *store) redeem(ctx context.Context, secret string, kind tokenKind, nowMs int64, check func(*token) error, access, refresh string, accessTTL, refreshTTL int64) (*token, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	t := &token{Kind: kind}
	err = tx.QueryRowContext(ctx, `SELECT client_id, user_id, redirect_uri, code_challenge, expires_at_ms FROM trading_connector_tokens
		WHERE hash = ? AND token_kind = ? AND expires_at_ms > ?`, hashSecret(secret), kind, nowMs).
		Scan(&t.ClientID, &t.UserID, &t.RedirectURI, &t.CodeChallenge, &t.ExpiresAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := check(t); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM trading_connector_tokens WHERE hash = ?`, hashSecret(secret))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errNotFound
	}
	base := token{ClientID: t.ClientID, UserID: t.UserID}
	a, r := base, base
	a.Kind, a.ExpiresAtMs = kindAccess, nowMs+accessTTL
	r.Kind, r.ExpiresAtMs = kindRefresh, nowMs+refreshTTL
	if err := s.putToken(ctx, tx, access, &a, nowMs); err != nil {
		return nil, err
	}
	if err := s.putToken(ctx, tx, refresh, &r, nowMs); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM trading_connector_tokens WHERE expires_at_ms <= ?`, nowMs); err != nil {
		return nil, err
	}
	return t, tx.Commit()
}
