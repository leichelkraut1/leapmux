package tradinglab

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// The connector passphrase signs a person in to the lab's read-only Claude
// connector (internal/worker/labmcp). It is not the confirmation passphrase:
// that one confirms orders and is never typed into a web page.

func getConnectorHash(ctx context.Context, q querier, userID string) (string, error) {
	var hash string
	err := q.QueryRowContext(ctx, `SELECT passphrase_hash FROM trading_connector_auth WHERE user_id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, err
}

// setConnectorPassphrase stores a new connector passphrase, which must differ
// from the confirmation passphrase, and signs out every connector session.
func setConnectorPassphrase(ctx context.Context, q querier, userID, pass, confirmationHash string, nowMs int64) error {
	if confirmationHash != "" && checkPassphrase(confirmationHash, pass) == nil {
		return invalidf("the connector passphrase must differ from the confirmation passphrase")
	}
	hash, err := hashPassphrase(pass)
	if err != nil {
		return invalidf("%s", strings.Replace(err.Error(), "confirmation", "connector", 1))
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO trading_connector_auth (user_id, passphrase_hash, updated_at_ms) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET passphrase_hash = excluded.passphrase_hash, updated_at_ms = excluded.updated_at_ms`,
		userID, hash, nowMs); err != nil {
		return err
	}
	return signOutConnector(ctx, q, userID)
}

func signOutConnector(ctx context.Context, q querier, userID string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM trading_connector_tokens WHERE user_id = ?`, userID)
	return err
}

// CheckConnectorPassphrase reports whether pass is the user's connector
// passphrase. It fails with ErrPassphrase when it is not, or when none is set.
func (l *Lab) CheckConnectorPassphrase(ctx context.Context, userID, pass string) error {
	hash, err := getConnectorHash(ctx, l.store.db, userID)
	if err != nil {
		return err
	}
	if hash == "" {
		return ErrPassphrase
	}
	return checkPassphrase(hash, pass)
}

// RunIDs lists the workflow runs the lab captured a snapshot for, newest
// first: the trading runs, whatever their template is called.
func (l *Lab) RunIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := l.store.db.QueryContext(ctx, `SELECT run_id, MAX(captured_at_ms) FROM trading_snapshots WHERE user_id = ? GROUP BY run_id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type row struct {
		id string
		at int64
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.at); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at > all[j].at })
	out := make([]string, len(all))
	for i, r := range all {
		out[i] = r.id
	}
	return out, nil
}
