package tradinglab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// The execution gateway is the only code that places orders, and it never
// acts on an AI recommendation by itself. An order moves
//
//	PREPARED -> CONFIRMED -> SUBMITTING -> SUBMITTED -> FILLED | PARTIALLY_FILLED | CANCELLED | REJECTED
//
// Preparing re-checks the account, the quote, the contract and the risk
// budget, and runs the broker's preview. Submitting needs either a person
// approving that exact order (its fingerprint) in their own browser before it
// expires, or an active execution policy whose scope covers it. Every
// transition is written before the broker call it leads to, and the client
// order id is unique, so an order is submitted at most once; after a crash
// mid-submission, reconcile asks the broker by that id and never resends.

const timeInForce = "gfd"

// ErrPaperMode is the gateway refusing a live order in paper mode.
var ErrPaperMode = errors.New("the lab is in paper mode, so the gateway places no live orders")

func (l *Lab) liveBroker(ctx context.Context, userID string, s *leapmuxv1.TradingLabSettings) (BrokerSession, error) {
	if s.GetMode() != leapmuxv1.TradingLabMode_TRADING_LAB_MODE_LIVE {
		return nil, fmt.Errorf("%w: %w", ErrConflict, ErrPaperMode)
	}
	sess, err := l.connect(ctx, userID, s.GetRobinhood())
	if err != nil {
		return nil, conflictf("the broker is unreachable: %v", err)
	}
	return sess, nil
}

// orderFingerprint is the SHA-256 of the exact order parameters, in
// canonical form. A person confirms this; a change to any parameter is a
// different order.
func orderFingerprint(o *leapmuxv1.TradingOrder) string {
	doc, _ := json.Marshal(map[string]any{
		"account_id": o.GetAccountId(), "occ_symbol": o.GetOccSymbol(), "side": o.GetSide().String(),
		"quantity": o.GetQuantity(), "limit_price": strconv.FormatFloat(o.GetLimitPrice(), 'f', 2, 64),
		"order_type": "limit", "time_in_force": o.GetTimeInForce(), "client_order_id": o.GetClientOrderId(),
	})
	_, sum, _ := workflow.CanonicalJSON(string(doc))
	return sum
}

// orderRequest is exactly the confirmed order, idempotency key included.
func orderRequest(o *leapmuxv1.TradingOrder) OrderRequest {
	return OrderRequest{
		AccountID: o.GetAccountId(), OCCSymbol: o.GetOccSymbol(), Side: o.GetSide(), Quantity: o.GetQuantity(),
		LimitPrice: o.GetLimitPrice(), TimeInForce: o.GetTimeInForce(), ClientOrderID: o.GetClientOrderId(),
	}
}

// openRisk sums the premium at risk in open live positions and in buy orders
// still working, and the risk opened today.
func (l *Lab) openRisk(ctx context.Context, q querier, userID string, now time.Time) (open, today float64, positions int, err error) {
	ps, err := listPositions(ctx, q, userID)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, p := range ps {
		if p.GetBook() == leapmuxv1.TradingBook_TRADING_BOOK_LIVE && p.GetState() == leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
			open += p.GetCost()
			positions++
		}
	}
	orders, err := listOrders(ctx, q, userID)
	if err != nil {
		return 0, 0, 0, err
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
	for _, o := range orders {
		if o.GetSide() != leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_BUY_TO_OPEN {
			continue
		}
		switch o.GetState() {
		case leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_CONFIRMED, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTING,
			leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PARTIALLY_FILLED:
			open += o.GetMaxLoss() * float64(o.GetQuantity()-o.GetFilledQuantity()) / float64(max(o.GetQuantity(), 1))
			if o.GetConfirmedAtMs() >= dayStart {
				today += o.GetMaxLoss()
			}
		case leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_FILLED:
			if o.GetConfirmedAtMs() >= dayStart {
				today += o.GetMaxLoss()
			}
		}
	}
	return open, today, positions, nil
}

// Prepare checks a proposed order and records it PREPARED. It places nothing.
func (l *Lab) Prepare(ctx context.Context, userID string, req *leapmuxv1.PrepareTradingOrderRequest) (*leapmuxv1.TradingOrder, error) {
	s, err := getSettings(ctx, l.store.db, userID)
	if err != nil {
		return nil, err
	}
	sess, err := l.liveBroker(ctx, userID, s)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	return l.prepareWith(ctx, userID, s, sess, req, "")
}

func (l *Lab) prepareWith(ctx context.Context, userID string, s *leapmuxv1.TradingLabSettings, broker TradingBroker, req *leapmuxv1.PrepareTradingOrderRequest, policyID string) (*leapmuxv1.TradingOrder, error) {
	now := l.now()
	risk := s.GetRisk()
	o := &leapmuxv1.TradingOrder{
		Id: l.newID(), State: leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PREPARED, TimeInForce: timeInForce,
		Quantity: req.GetQuantity(), LimitPrice: round2(req.GetLimitPrice()), PolicyId: policyID,
		CreatedAtMs: now.UnixMilli(), UpdatedAtMs: now.UnixMilli(),
	}
	o.ClientOrderId = o.GetId() // the broker's ref: one per prepared order
	check := func(format string, args ...any) { o.Checks = append(o.Checks, fmt.Sprintf(format, args...)) }

	var pos *leapmuxv1.TradingPosition
	switch {
	case req.GetCandidateId() != "" && req.GetPositionId() != "":
		return nil, invalidf("an order opens a candidate or closes a position, not both")
	case req.GetCandidateId() != "":
		c, err := getCandidate(ctx, l.store.db, userID, req.GetCandidateId())
		if err != nil {
			return nil, err
		}
		if c.GetStatus() != leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_APPROVED {
			return nil, conflictf("only a candidate in the approved queue can be ordered")
		}
		o.CandidateId, o.OccSymbol, o.Side = c.GetId(), c.GetOccSymbol(), leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_BUY_TO_OPEN
		if o.GetLimitPrice() == 0 {
			o.LimitPrice = c.GetEntryLimit()
		}
	case req.GetPositionId() != "":
		p, err := getPosition(ctx, l.store.db, userID, req.GetPositionId())
		if err != nil {
			return nil, err
		}
		if p.GetBook() != leapmuxv1.TradingBook_TRADING_BOOK_LIVE || p.GetState() != leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
			return nil, conflictf("only an open live position can be closed through the broker")
		}
		pos = p
		o.PositionId, o.OccSymbol, o.Side = p.GetId(), p.GetOccSymbol(), leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_SELL_TO_CLOSE
		if o.GetQuantity() == 0 {
			o.Quantity = p.GetQuantity()
		}
		if o.GetQuantity() > p.GetQuantity() {
			return nil, invalidf("the position holds %d contracts, fewer than %d", p.GetQuantity(), o.GetQuantity())
		}
	default:
		return nil, invalidf("name the approved candidate to open or the live position to close")
	}
	if o.GetQuantity() == 0 {
		return nil, invalidf("the quantity must be at least 1")
	}
	if o.GetLimitPrice() <= 0 {
		return nil, invalidf("a limit price is required")
	}

	acct, err := broker.Account(ctx)
	if err != nil {
		return nil, conflictf("the account read failed: %v", err)
	}
	o.AccountId = acct.ID
	if !optionsLevelAllowsLong(acct.OptionsLevel) {
		return nil, conflictf("the account's options level %q does not allow buying options", acct.OptionsLevel)
	}
	check("account %s may trade long options (level %s)", maskAccount(acct.ID), acct.OptionsLevel)

	quotes, err := broker.OptionQuotes(ctx, []string{o.GetOccSymbol()})
	if err != nil {
		return nil, conflictf("the quote refresh failed: %v", err)
	}
	var q *OptionQuote
	for i := range quotes {
		if quotes[i].OCCSymbol == o.GetOccSymbol() {
			q = &quotes[i]
		}
	}
	if q == nil {
		return nil, conflictf("the broker does not list %s", o.GetOccSymbol())
	}
	if !q.Tradable {
		return nil, conflictf("%s is not tradable", o.GetOccSymbol())
	}
	exp, _ := time.Parse(time.DateOnly, q.Expiration)
	if !now.Before(exp.Add(expirySettlement)) {
		return nil, conflictf("%s has expired", o.GetOccSymbol())
	}
	age := now.Sub(q.QuoteTime)
	if q.QuoteTime.IsZero() || age > time.Duration(risk.GetMaxQuoteAgeSeconds())*time.Second {
		return nil, conflictf("the quote for %s is %s old, older than %ds", o.GetOccSymbol(), age.Round(time.Second), risk.GetMaxQuoteAgeSeconds())
	}
	if q.Bid <= 0 || q.Ask <= 0 {
		return nil, conflictf("%s has no two-sided quote", o.GetOccSymbol())
	}
	o.Bid, o.Ask, o.QuoteAtMs = q.Bid, q.Ask, q.QuoteTime.UnixMilli()
	check("quote %.2f x %.2f, %s old", q.Bid, q.Ask, age.Round(time.Second))

	if o.GetQuantity() > risk.GetMaxContractsPerOrder() {
		return nil, conflictf("%d contracts exceed the limit of %d per order", o.GetQuantity(), risk.GetMaxContractsPerOrder())
	}
	if o.GetSide() == leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_BUY_TO_OPEN {
		if o.GetLimitPrice() > q.Ask*1.1+0.05 {
			return nil, conflictf("the limit %.2f is more than 10%% above the ask %.2f", o.GetLimitPrice(), q.Ask)
		}
		if o.GetLimitPrice() < q.Bid {
			return nil, conflictf("the limit %.2f is below the bid %.2f", o.GetLimitPrice(), q.Bid)
		}
	} else if o.GetLimitPrice() > q.Ask*1.5 {
		return nil, conflictf("the sale limit %.2f is far above the ask %.2f and will not fill", o.GetLimitPrice(), q.Ask)
	}

	preview, err := broker.Preview(ctx, OrderRequest{
		AccountID: o.GetAccountId(), OCCSymbol: o.GetOccSymbol(), Side: o.GetSide(), Quantity: o.GetQuantity(),
		LimitPrice: o.GetLimitPrice(), TimeInForce: o.GetTimeInForce(),
	})
	if err != nil {
		return nil, conflictf("the broker's preview failed: %v", err)
	}
	o.EstimatedFees, o.BrokerAlerts = round2(preview.EstimatedFees), preview.Alerts
	check("broker preview: fees %.2f, %d alert(s)", o.GetEstimatedFees(), len(o.GetBrokerAlerts()))

	if o.GetSide() == leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_BUY_TO_OPEN {
		o.MaxLoss = round2(o.GetLimitPrice()*100*float64(o.GetQuantity()) + o.GetEstimatedFees())
		if o.GetMaxLoss() > risk.GetMaxLossPerOrder() {
			return nil, conflictf("the maximum loss %.2f exceeds the limit of %.2f per order", o.GetMaxLoss(), risk.GetMaxLossPerOrder())
		}
		open, today, positions, err := l.openRisk(ctx, l.store.db, userID, now)
		if err != nil {
			return nil, err
		}
		if open+o.GetMaxLoss() > risk.GetMaxOpenRisk() {
			return nil, conflictf("open risk would be %.2f, over the limit of %.2f", open+o.GetMaxLoss(), risk.GetMaxOpenRisk())
		}
		if today+o.GetMaxLoss() > risk.GetMaxDailyRisk() {
			return nil, conflictf("today's risk would be %.2f, over the limit of %.2f", today+o.GetMaxLoss(), risk.GetMaxDailyRisk())
		}
		if positions >= int(risk.GetMaxOpenPositions()) {
			return nil, conflictf("%d live positions are open, the limit", positions)
		}
		if acct.BuyingPower-o.GetMaxLoss() < risk.GetMinBuyingPowerReserve() {
			return nil, conflictf("buying power %.2f less the order's %.2f leaves less than the %.2f reserve", acct.BuyingPower, o.GetMaxLoss(), risk.GetMinBuyingPowerReserve())
		}
		check("risk: %.2f max loss; open risk %.2f of %.2f; today %.2f of %.2f; buying power %.2f", o.GetMaxLoss(), open+o.GetMaxLoss(), risk.GetMaxOpenRisk(),
			today+o.GetMaxLoss(), risk.GetMaxDailyRisk(), acct.BuyingPower)
	} else {
		check("closing %d of %d contracts", o.GetQuantity(), pos.GetQuantity())
	}
	o.Fingerprint = orderFingerprint(o)
	o.ConfirmByMs = now.Add(time.Duration(risk.GetConfirmationTtlSeconds()) * time.Second).UnixMilli()
	o.Detail = "Waiting for a person to confirm this exact order"
	if err := insertOrder(ctx, l.store.db, userID, o); err != nil {
		return nil, err
	}
	return o, nil
}

// Confirm submits a prepared order that a person approved. byPerson is set
// only for a call from the person's own browser session: never a control
// socket, an API token, an app or an agent's delegation, which all hold the
// owner's identity and so cannot be told apart from them otherwise.
func (l *Lab) Confirm(ctx context.Context, userID string, req *leapmuxv1.ConfirmTradingOrderRequest, byPerson bool) (*leapmuxv1.TradingOrder, error) {
	if !byPerson {
		return nil, deniedf("only you can approve an order, in the Trading Research view in your browser; an agent or app cannot")
	}
	s, err := getSettings(ctx, l.store.db, userID)
	if err != nil {
		return nil, err
	}
	sess, err := l.liveBroker(ctx, userID, s)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	return l.submit(ctx, userID, sess, req.GetOrderId(), req.GetFingerprint(), "user")
}

// submit moves a PREPARED order to SUBMITTING under the gateway lock, then
// places it. Only one caller ever wins the PREPARED state.
func (l *Lab) submit(ctx context.Context, userID string, broker TradingBroker, orderID, fingerprint, who string) (*leapmuxv1.TradingOrder, error) {
	l.gateMu.Lock()
	var o *leapmuxv1.TradingOrder
	expired := false
	err := l.store.inTx(ctx, func(q querier) error {
		var err error
		if o, err = getOrder(ctx, q, userID, orderID); err != nil {
			return err
		}
		if o.GetState() != leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PREPARED {
			return conflictf("the order is already %s", stateWord(o.GetState()))
		}
		now := l.now()
		if now.UnixMilli() > o.GetConfirmByMs() {
			o.State, o.Detail, o.UpdatedAtMs = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_EXPIRED, "Not confirmed in time; prepare it again", now.UnixMilli()
			expired = true
			return updateOrder(ctx, q, userID, o)
		}
		if fingerprint != o.GetFingerprint() || orderFingerprint(o) != o.GetFingerprint() {
			return deniedf("the confirmation does not match this exact order; prepare it again")
		}
		o.State, o.ConfirmedBy, o.ConfirmedAtMs = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTING, who, now.UnixMilli()
		o.Detail, o.UpdatedAtMs = "Submitting to the broker", now.UnixMilli()
		return updateOrder(ctx, q, userID, o)
	})
	l.gateMu.Unlock()
	if err != nil {
		return nil, err
	}
	if expired {
		return o, conflictf("the confirmation window closed; prepare the order again")
	}
	placed, placeErr := broker.Place(ctx, orderRequest(o))
	now := l.now().UnixMilli()
	err = l.store.inTx(ctx, func(q querier) error {
		cur, err := getOrder(ctx, q, userID, orderID)
		if err != nil {
			return err
		}
		switch {
		case placeErr == nil:
			cur.BrokerOrderId, cur.State, cur.Detail = placed.ID, placed.State, "Submitted"
			if placed.State == leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_UNSPECIFIED {
				cur.State = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED
			}
		case errors.Is(placeErr, ErrRejected):
			cur.State, cur.Detail = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_REJECTED, placeErr.Error()
		default:
			// The outcome is unknown: the request may have reached the
			// broker. The order stays SUBMITTING until reconcile finds it.
			cur.Detail = "The submission's outcome is unknown (" + placeErr.Error() + "); reconcile asks the broker before anything is sent again"
		}
		cur.UpdatedAtMs = now
		if err := updateOrder(ctx, q, userID, cur); err != nil {
			return err
		}
		if cur.GetCandidateId() != "" && cur.GetState() != leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_REJECTED {
			if c, err := getCandidate(ctx, q, userID, cur.GetCandidateId()); err == nil {
				c.Status = leapmuxv1.TradingCandidateStatus_TRADING_CANDIDATE_STATUS_ORDERED
				if err := putCandidate(ctx, q, userID, c, now); err != nil {
					return err
				}
			}
		}
		o = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	if placed != nil {
		if _, err := l.applyBrokerOrder(ctx, userID, o, placed); err != nil {
			return nil, err
		}
		o, _ = getOrder(ctx, l.store.db, userID, orderID)
	}
	return o, nil
}

func stateWord(s leapmuxv1.TradingOrderState) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(s.String(), "TRADING_ORDER_STATE_"), "_", " "))
}

// Cancel withdraws an order: a PREPARED one is dropped, a working one is
// cancelled at the broker. A SUBMITTING one must be reconciled first.
func (l *Lab) Cancel(ctx context.Context, userID, orderID string) (*leapmuxv1.TradingOrder, error) {
	o, err := getOrder(ctx, l.store.db, userID, orderID)
	if err != nil {
		return nil, err
	}
	switch o.GetState() {
	case leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PREPARED:
		o.State, o.Detail, o.UpdatedAtMs = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_CANCELLED, "Withdrawn before confirmation", l.now().UnixMilli()
		return o, updateOrder(ctx, l.store.db, userID, o)
	case leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PARTIALLY_FILLED:
	case leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTING:
		return nil, conflictf("the submission's outcome is unknown; reconcile first")
	default:
		return nil, conflictf("the order is already %s", stateWord(o.GetState()))
	}
	s, err := l.Settings(ctx, userID)
	if err != nil {
		return nil, err
	}
	sess, err := l.connect(ctx, userID, s.GetRobinhood())
	if err != nil {
		return nil, conflictf("the broker is unreachable: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Cancel(ctx, o.GetBrokerOrderId()); err != nil {
		return nil, conflictf("the broker refused the cancel: %v", err)
	}
	o.Detail, o.UpdatedAtMs = "Cancel requested; reconcile confirms it", l.now().UnixMilli()
	if err := updateOrder(ctx, l.store.db, userID, o); err != nil {
		return nil, err
	}
	return o, nil
}

// Reconcile asks the broker about every order whose outcome is not final and
// records what it says: states, fills and the live positions they make.
func (l *Lab) Reconcile(ctx context.Context, userID string) ([]*leapmuxv1.TradingOrder, error) {
	orders, err := listOrders(ctx, l.store.db, userID)
	if err != nil {
		return nil, err
	}
	var pending []*leapmuxv1.TradingOrder
	now := l.now().UnixMilli()
	for _, o := range orders {
		switch o.GetState() {
		case leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTING, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_SUBMITTED,
			leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PARTIALLY_FILLED, leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_CONFIRMED:
			pending = append(pending, o)
		case leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_PREPARED:
			if now > o.GetConfirmByMs() {
				o.State, o.Detail, o.UpdatedAtMs = leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_EXPIRED, "Not confirmed in time", now
				if err := updateOrder(ctx, l.store.db, userID, o); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(pending) > 0 {
		s, err := l.Settings(ctx, userID)
		if err != nil {
			return nil, err
		}
		sess, err := l.connect(ctx, userID, s.GetRobinhood())
		if err != nil {
			return nil, conflictf("the broker is unreachable: %v", err)
		}
		defer func() { _ = sess.Close() }()
		for _, o := range pending {
			var bo *BrokerOrder
			if o.GetBrokerOrderId() != "" {
				bo, err = sess.Order(ctx, o.GetBrokerOrderId())
			} else {
				bo, err = sess.FindOrder(ctx, o.GetClientOrderId())
			}
			if err != nil {
				l.log.Warn("reconcile order", "order", o.GetId(), "error", err)
				continue
			}
			if bo == nil {
				// Never resubmitted: an order the broker cannot find stays
				// for a person to resolve.
				o.Detail, o.UpdatedAtMs = "The broker has no order with this reference yet; it was not resent", now
				if err := updateOrder(ctx, l.store.db, userID, o); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := l.applyBrokerOrder(ctx, userID, o, bo); err != nil {
				return nil, err
			}
		}
	}
	return listOrders(ctx, l.store.db, userID)
}

// applyBrokerOrder records the broker's view of an order: its state, its
// new fills (each once), and the live position they open, add to or close.
func (l *Lab) applyBrokerOrder(ctx context.Context, userID string, o *leapmuxv1.TradingOrder, bo *BrokerOrder) (*leapmuxv1.TradingOrder, error) {
	var out *leapmuxv1.TradingOrder
	err := l.store.inTx(ctx, func(q querier) error {
		cur, err := getOrder(ctx, q, userID, o.GetId())
		if err != nil {
			return err
		}
		now := l.now().UnixMilli()
		if bo.ID != "" {
			cur.BrokerOrderId = bo.ID
		}
		if bo.State != leapmuxv1.TradingOrderState_TRADING_ORDER_STATE_UNSPECIFIED {
			cur.State = bo.State
		}
		if bo.Detail != "" {
			cur.Detail = bo.Detail
		} else {
			cur.Detail = "Reconciled with the broker"
		}
		fills := bo.Fills
		if len(fills) == 0 && bo.FilledQty > 0 {
			fills = []BrokerFill{{Key: "cumulative", Quantity: bo.FilledQty, Price: bo.AveragePrice}}
		}
		for _, f := range fills {
			fresh, err := insertLiveFill(ctx, q, cur.GetId(), f.Key, f.Quantity, f.Price, f.Fees, max(f.At.UnixMilli(), now))
			if err != nil {
				return err
			}
			if fresh {
				if err := l.applyFill(ctx, q, userID, cur, f, now); err != nil {
					return err
				}
				prevQty := float64(cur.GetFilledQuantity())
				cur.FilledQuantity += f.Quantity
				cur.AverageFillPrice = round4((cur.GetAverageFillPrice()*prevQty + f.Price*float64(f.Quantity)) / float64(cur.GetFilledQuantity()))
			}
		}
		cur.UpdatedAtMs = now
		out = cur
		return updateOrder(ctx, q, userID, cur)
	})
	return out, err
}

// applyFill turns one real fill into the LIVE book.
func (l *Lab) applyFill(ctx context.Context, q querier, userID string, o *leapmuxv1.TradingOrder, f BrokerFill, now int64) error {
	feesPerFill := f.Fees
	if o.GetSide() == leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_BUY_TO_OPEN {
		id := "live-" + o.GetId()
		p, err := getPosition(ctx, q, userID, id)
		if errors.Is(err, ErrNotFound) {
			parsed, _ := ParseOCC(o.GetOccSymbol())
			p = &leapmuxv1.TradingPosition{
				Id: id, Book: leapmuxv1.TradingBook_TRADING_BOOK_LIVE, CandidateId: o.GetCandidateId(), OccSymbol: o.GetOccSymbol(),
				Underlying: parsed.Root, Right: parsed.Right, Strike: parsed.Strike, Expiration: parsed.Expiration,
				State: leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN, OpenedAtMs: now, LastMarkAtMs: now,
			}
			if c, err := getCandidate(ctx, q, userID, o.GetCandidateId()); err == nil {
				p.RunId, p.ForecastProbability = c.GetRunId(), c.GetProbabilityProfit()
			}
		} else if err != nil {
			return err
		}
		total := p.GetEntryPrice()*float64(p.GetQuantity()) + f.Price*float64(f.Quantity)
		p.Quantity += f.Quantity
		p.EntryPrice = round4(total / float64(p.GetQuantity()))
		p.Fees = round2(p.GetFees() + feesPerFill)
		p.Cost = round2(p.GetCost() + f.Price*100*float64(f.Quantity) + feesPerFill)
		p.LastMark = p.GetCost()
		return putPosition(ctx, q, userID, p, now)
	}
	p, err := getPosition(ctx, q, userID, o.GetPositionId())
	if err != nil {
		return err
	}
	if f.Quantity > p.GetQuantity() {
		return fmt.Errorf("a fill of %d exceeds the %d contracts held", f.Quantity, p.GetQuantity())
	}
	share := p.GetCost() * float64(f.Quantity) / float64(p.GetQuantity())
	proceeds := f.Price*100*float64(f.Quantity) - feesPerFill
	p.RealizedPnl = round2(p.GetRealizedPnl() + proceeds - share)
	p.Cost = round2(p.GetCost() - share)
	p.Quantity -= f.Quantity
	p.ExitPrice = f.Price
	if p.GetQuantity() == 0 {
		p.State, p.ClosedAtMs, p.Note = leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_CLOSED, now, "Closed by order "+o.GetId()
		p.Cost = round2(share + p.GetCost())
	}
	return putPosition(ctx, q, userID, p, now)
}

// --- Execution policies ---

// SavePolicy approves a standing, precisely scoped policy. It is the user's
// decision as much as a confirmation is, so like Confirm it needs byPerson.
// The lab supports only closing policies: a policy may sell an open live
// position at or above a price, never buy.
func (l *Lab) SavePolicy(ctx context.Context, userID string, req *leapmuxv1.SaveTradingExecutionPolicyRequest, byPerson bool) (*leapmuxv1.TradingExecutionPolicy, error) {
	if !byPerson {
		return nil, deniedf("only you can approve an execution policy, in your browser; an agent or app cannot")
	}
	p := proto.Clone(req.GetPolicy()).(*leapmuxv1.TradingExecutionPolicy)
	if p == nil {
		return nil, invalidf("missing policy")
	}
	if p.GetSide() != leapmuxv1.TradingOrderSide_TRADING_ORDER_SIDE_SELL_TO_CLOSE {
		return nil, invalidf("a policy may only close a position; every opening order needs its own confirmation")
	}
	pos, err := getPosition(ctx, l.store.db, userID, p.GetPositionId())
	if err != nil {
		return nil, err
	}
	if pos.GetBook() != leapmuxv1.TradingBook_TRADING_BOOK_LIVE || pos.GetState() != leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
		return nil, conflictf("a policy can only cover an open live position")
	}
	now := l.now()
	switch {
	case p.GetMaxQuantity() == 0 || p.GetMaxQuantity() > pos.GetQuantity():
		return nil, invalidf("the policy's quantity must be between 1 and the %d contracts held", pos.GetQuantity())
	case p.GetLimitPriceBound() <= 0:
		return nil, invalidf("the policy needs its lowest acceptable sale price")
	case p.GetExpiresAtMs() <= now.UnixMilli() || p.GetExpiresAtMs() > now.Add(7*24*time.Hour).UnixMilli():
		return nil, invalidf("a policy must expire within seven days")
	}
	p.Id, p.OccSymbol, p.Active, p.ApprovedAtMs, p.UsedAtMs = l.newID(), pos.GetOccSymbol(), true, now.UnixMilli(), 0
	return p, putPolicy(ctx, l.store.db, userID, p)
}

// RevokePolicy ends a policy at once.
func (l *Lab) RevokePolicy(ctx context.Context, userID, policyID string) (*leapmuxv1.TradingExecutionPolicy, error) {
	p, err := getPolicy(ctx, l.store.db, userID, policyID)
	if err != nil {
		return nil, err
	}
	p.Active = false
	return p, putPolicy(ctx, l.store.db, userID, p)
}

// runPolicies lets each active policy close its position when the live bid
// reaches its bound. A policy is used once. It returns what it could not do.
func (l *Lab) runPolicies(ctx context.Context, userID string, s *leapmuxv1.TradingLabSettings) []string {
	if s.GetMode() != leapmuxv1.TradingLabMode_TRADING_LAB_MODE_LIVE {
		return nil
	}
	policies, err := listPolicies(ctx, l.store.db, userID)
	if err != nil {
		return []string{err.Error()}
	}
	var notes []string
	now := l.now()
	for _, p := range policies {
		if !p.GetActive() {
			continue
		}
		if now.UnixMilli() > p.GetExpiresAtMs() {
			p.Active = false
			_ = putPolicy(ctx, l.store.db, userID, p)
			continue
		}
		pos, err := getPosition(ctx, l.store.db, userID, p.GetPositionId())
		if err != nil || pos.GetState() != leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
			p.Active = false
			_ = putPolicy(ctx, l.store.db, userID, p)
			continue
		}
		sess, err := l.liveBroker(ctx, userID, s)
		if err != nil {
			notes = append(notes, "policy "+p.GetId()+": "+err.Error())
			continue
		}
		quotes, err := sess.OptionQuotes(ctx, []string{p.GetOccSymbol()})
		if err != nil || len(quotes) == 0 || quotes[0].Bid+1e-9 < p.GetLimitPriceBound() {
			_ = sess.Close()
			continue
		}
		qty := min(p.GetMaxQuantity(), pos.GetQuantity())
		o, err := l.prepareWith(ctx, userID, s, sess, &leapmuxv1.PrepareTradingOrderRequest{
			PositionId: p.GetPositionId(), Quantity: qty, LimitPrice: math.Max(p.GetLimitPriceBound(), round2(quotes[0].Bid)),
		}, p.GetId())
		if err == nil {
			// The policy is the authorization: its scope was checked by
			// prepare (an open live position, a sale, at or above the bound).
			p.Active, p.UsedAtMs = false, now.UnixMilli()
			if err = putPolicy(ctx, l.store.db, userID, p); err == nil {
				_, err = l.submit(ctx, userID, sess, o.GetId(), o.GetFingerprint(), "policy "+p.GetId())
			}
		}
		if err != nil {
			notes = append(notes, "policy "+p.GetId()+": "+err.Error())
		}
		_ = sess.Close()
	}
	return notes
}
