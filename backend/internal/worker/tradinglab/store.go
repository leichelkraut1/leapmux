package tradinglab

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ErrNotFound is a row that does not exist for this user.
var ErrNotFound = errors.New("not found")

// store persists the lab in the worker database. Enum ordinals are bound as
// parameters from their proto constants.
type store struct{ db *sql.DB }

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *store) inTx(ctx context.Context, fn func(q querier) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func marshal(m proto.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func scanSpec[T proto.Message](rows *sql.Rows, newT func() T) ([]T, error) {
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, err
		}
		m := newT()
		if err := proto.Unmarshal(spec, m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- Settings ---

func getSettings(ctx context.Context, q querier, userID string) (*leapmuxv1.TradingLabSettings, error) {
	var spec []byte
	err := q.QueryRowContext(ctx, `SELECT spec FROM trading_lab_settings WHERE user_id = ?`, userID).Scan(&spec)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return nil, err
	}
	s := &leapmuxv1.TradingLabSettings{}
	if err := proto.Unmarshal(spec, s); err != nil {
		return nil, err
	}
	return s, nil
}

// putSettings stores settings. passphrase_hash held the order confirmation
// passphrase, which approval in the person's browser replaced; writing it
// empty clears any a deployed worker kept.
func putSettings(ctx context.Context, q querier, userID string, s *leapmuxv1.TradingLabSettings) error {
	spec, err := marshal(s)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO trading_lab_settings (user_id, spec, passphrase_hash, updated_at_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET spec = excluded.spec, passphrase_hash = excluded.passphrase_hash, updated_at_ms = excluded.updated_at_ms`,
		userID, spec, "", s.GetUpdatedAtMs())
	return err
}

func getBrokerCheck(ctx context.Context, q querier, userID string) (*leapmuxv1.TradingBrokerStatus, error) {
	var spec []byte
	err := q.QueryRowContext(ctx, `SELECT spec FROM trading_broker_checks WHERE user_id = ?`, userID).Scan(&spec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &leapmuxv1.TradingBrokerStatus{}
	return s, proto.Unmarshal(spec, s)
}

func putBrokerCheck(ctx context.Context, q querier, userID string, s *leapmuxv1.TradingBrokerStatus) error {
	spec, err := marshal(s)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO trading_broker_checks (user_id, spec, checked_at_ms) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET spec = excluded.spec, checked_at_ms = excluded.checked_at_ms`, userID, spec, s.GetCheckedAtMs())
	return err
}

// --- Snapshots ---

type storedSnapshot struct {
	id, runID, taskID, sha256, canonical string
	asOfMs, capturedAtMs                 int64
	source                               leapmuxv1.TradingMarketDataSource
}

func snapshotForTask(ctx context.Context, q querier, taskID string) (*storedSnapshot, error) {
	s := &storedSnapshot{}
	var source int32
	err := q.QueryRowContext(ctx, `SELECT id, run_id, task_id, sha256, canonical_json, as_of_ms, captured_at_ms, source FROM trading_snapshots WHERE task_id = ?`, taskID).
		Scan(&s.id, &s.runID, &s.taskID, &s.sha256, &s.canonical, &s.asOfMs, &s.capturedAtMs, &source)
	if err != nil {
		return nil, notFound(err)
	}
	s.source = leapmuxv1.TradingMarketDataSource(source)
	return s, nil
}

func snapshotForRun(ctx context.Context, q querier, userID, runID string) (*storedSnapshot, error) {
	var taskID string
	if err := q.QueryRowContext(ctx, `SELECT task_id FROM trading_snapshots WHERE run_id = ? AND user_id = ? ORDER BY captured_at_ms DESC LIMIT 1`, runID, userID).Scan(&taskID); err != nil {
		return nil, notFound(err)
	}
	return snapshotForTask(ctx, q, taskID)
}

func insertSnapshot(ctx context.Context, q querier, userID string, s *storedSnapshot) error {
	_, err := q.ExecContext(ctx, `INSERT INTO trading_snapshots (id, user_id, run_id, task_id, sha256, as_of_ms, source, canonical_json, captured_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.id, userID, s.runID, s.taskID, s.sha256, s.asOfMs, int32(s.source), s.canonical, s.capturedAtMs)
	return err
}

// --- Runs ---

func getRunView(ctx context.Context, q querier, userID, runID string) (*leapmuxv1.TradingLabRunView, error) {
	var spec []byte
	if err := q.QueryRowContext(ctx, `SELECT view FROM trading_runs WHERE run_id = ? AND user_id = ?`, runID, userID).Scan(&spec); err != nil {
		return nil, notFound(err)
	}
	v := &leapmuxv1.TradingLabRunView{}
	return v, proto.Unmarshal(spec, v)
}

func insertRunView(ctx context.Context, q querier, userID, snapshotID string, v *leapmuxv1.TradingLabRunView, nowMs int64) error {
	spec, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO trading_runs (run_id, user_id, snapshot_id, view, recorded_at_ms) VALUES (?, ?, ?, ?, ?)`,
		v.GetRunId(), userID, snapshotID, spec, nowMs)
	return err
}

// --- Candidates ---

func putCandidate(ctx context.Context, q querier, userID string, c *leapmuxv1.TradingCandidate, nowMs int64) error {
	spec, err := marshal(c)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO trading_candidates (id, user_id, run_id, source, recommendation_id, status, spec, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, spec = excluded.spec, updated_at_ms = excluded.updated_at_ms
		WHERE trading_candidates.user_id = excluded.user_id`,
		c.GetId(), userID, c.GetRunId(), int32(c.GetSource()), c.GetRecommendationId(), int32(c.GetStatus()), spec, c.GetCreatedAtMs(), nowMs)
	return err
}

func getCandidate(ctx context.Context, q querier, userID, id string) (*leapmuxv1.TradingCandidate, error) {
	var spec []byte
	if err := q.QueryRowContext(ctx, `SELECT spec FROM trading_candidates WHERE id = ? AND user_id = ?`, id, userID).Scan(&spec); err != nil {
		return nil, notFound(err)
	}
	c := &leapmuxv1.TradingCandidate{}
	return c, proto.Unmarshal(spec, c)
}

func listCandidatesForRun(ctx context.Context, q querier, userID, runID string) ([]*leapmuxv1.TradingCandidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT spec FROM trading_candidates WHERE user_id = ? AND run_id = ? ORDER BY source, recommendation_id`, userID, runID)
	if err != nil {
		return nil, err
	}
	return scanSpec(rows, func() *leapmuxv1.TradingCandidate { return &leapmuxv1.TradingCandidate{} })
}

func listQueue(ctx context.Context, q querier, userID string) ([]*leapmuxv1.TradingCandidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT spec FROM trading_candidates WHERE user_id = ? AND status IN (?, ?) ORDER BY created_at_ms DESC, id`,
		userID, int32(leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ELIGIBLE), int32(leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_APPROVED))
	if err != nil {
		return nil, err
	}
	return scanSpec(rows, func() *leapmuxv1.TradingCandidate { return &leapmuxv1.TradingCandidate{} })
}

// --- Positions ---

func putPosition(ctx context.Context, q querier, userID string, p *leapmuxv1.TradingPosition, nowMs int64) error {
	spec, err := marshal(p)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO trading_positions (id, user_id, book, candidate_id, state, spec, opened_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET state = excluded.state, spec = excluded.spec, updated_at_ms = excluded.updated_at_ms
		WHERE trading_positions.user_id = excluded.user_id`,
		p.GetId(), userID, int32(p.GetBook()), p.GetCandidateId(), int32(p.GetState()), spec, p.GetOpenedAtMs(), nowMs)
	return err
}

func getPosition(ctx context.Context, q querier, userID, id string) (*leapmuxv1.TradingPosition, error) {
	var spec []byte
	if err := q.QueryRowContext(ctx, `SELECT spec FROM trading_positions WHERE id = ? AND user_id = ?`, id, userID).Scan(&spec); err != nil {
		return nil, notFound(err)
	}
	p := &leapmuxv1.TradingPosition{}
	return p, proto.Unmarshal(spec, p)
}

func listPositions(ctx context.Context, q querier, userID string) ([]*leapmuxv1.TradingPosition, error) {
	rows, err := q.QueryContext(ctx, `SELECT spec FROM trading_positions WHERE user_id = ? ORDER BY opened_at_ms DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	return scanSpec(rows, func() *leapmuxv1.TradingPosition { return &leapmuxv1.TradingPosition{} })
}

func insertMarks(ctx context.Context, q querier, positionID string, marks []Mark) error {
	for _, m := range marks {
		missing := 0
		if m.Missing {
			missing = 1
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO trading_marks (position_id, at_ms, horizon_days, bid, underlying_price, value, missing) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (position_id, at_ms, horizon_days) DO NOTHING`,
			positionID, m.AtMs, m.HorizonDays, m.Bid, m.UnderlyingPrice, m.Value, missing); err != nil {
			return err
		}
	}
	return nil
}

func listMarks(ctx context.Context, q querier, userID string) (map[string][]Mark, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.position_id, m.at_ms, m.horizon_days, m.bid, m.underlying_price, m.value, m.missing
		FROM trading_marks m JOIN trading_positions p ON p.id = m.position_id WHERE p.user_id = ? ORDER BY m.at_ms`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]Mark{}
	for rows.Next() {
		var id string
		var m Mark
		var missing int
		if err := rows.Scan(&id, &m.AtMs, &m.HorizonDays, &m.Bid, &m.UnderlyingPrice, &m.Value, &missing); err != nil {
			return nil, err
		}
		m.Missing = missing == 1
		out[id] = append(out[id], m)
	}
	return out, rows.Err()
}

// --- Orders ---

func insertOrder(ctx context.Context, q querier, userID string, o *leapmuxv1.TradingOrder) error {
	spec, err := marshal(o)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO trading_orders (id, user_id, state, side, client_order_id, spec, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		o.GetId(), userID, int32(o.GetState()), int32(o.GetSide()), o.GetClientOrderId(), spec, o.GetCreatedAtMs(), o.GetUpdatedAtMs())
	return err
}

func updateOrder(ctx context.Context, q querier, userID string, o *leapmuxv1.TradingOrder) error {
	spec, err := marshal(o)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `UPDATE trading_orders SET state = ?, spec = ?, updated_at_ms = ? WHERE id = ? AND user_id = ?`,
		int32(o.GetState()), spec, o.GetUpdatedAtMs(), o.GetId(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func getOrder(ctx context.Context, q querier, userID, id string) (*leapmuxv1.TradingOrder, error) {
	var spec []byte
	if err := q.QueryRowContext(ctx, `SELECT spec FROM trading_orders WHERE id = ? AND user_id = ?`, id, userID).Scan(&spec); err != nil {
		return nil, notFound(err)
	}
	o := &leapmuxv1.TradingOrder{}
	return o, proto.Unmarshal(spec, o)
}

func listOrders(ctx context.Context, q querier, userID string) ([]*leapmuxv1.TradingOrder, error) {
	rows, err := q.QueryContext(ctx, `SELECT spec FROM trading_orders WHERE user_id = ? ORDER BY created_at_ms DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	return scanSpec(rows, func() *leapmuxv1.TradingOrder { return &leapmuxv1.TradingOrder{} })
}

func insertLiveFill(ctx context.Context, q querier, orderID, key string, qty uint32, price, fees float64, atMs int64) (bool, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO trading_live_fills (order_id, fill_key, quantity, price, fees, at_ms) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (order_id, fill_key) DO NOTHING`, orderID, key, qty, price, fees, atMs)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// --- Policies ---

func putPolicy(ctx context.Context, q querier, userID string, p *leapmuxv1.TradingExecutionPolicy) error {
	spec, err := marshal(p)
	if err != nil {
		return err
	}
	active := 0
	if p.GetActive() {
		active = 1
	}
	_, err = q.ExecContext(ctx, `INSERT INTO trading_policies (id, user_id, active, spec, expires_at_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET active = excluded.active, spec = excluded.spec WHERE trading_policies.user_id = excluded.user_id`,
		p.GetId(), userID, active, spec, p.GetExpiresAtMs())
	return err
}

func getPolicy(ctx context.Context, q querier, userID, id string) (*leapmuxv1.TradingExecutionPolicy, error) {
	var spec []byte
	if err := q.QueryRowContext(ctx, `SELECT spec FROM trading_policies WHERE id = ? AND user_id = ?`, id, userID).Scan(&spec); err != nil {
		return nil, notFound(err)
	}
	p := &leapmuxv1.TradingExecutionPolicy{}
	return p, proto.Unmarshal(spec, p)
}

func listPolicies(ctx context.Context, q querier, userID string) ([]*leapmuxv1.TradingExecutionPolicy, error) {
	rows, err := q.QueryContext(ctx, `SELECT spec FROM trading_policies WHERE user_id = ? ORDER BY expires_at_ms DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	return scanSpec(rows, func() *leapmuxv1.TradingExecutionPolicy { return &leapmuxv1.TradingExecutionPolicy{} })
}
