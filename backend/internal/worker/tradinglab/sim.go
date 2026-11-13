package tradinglab

import (
	"fmt"
	"math"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The simulator fills from recorded quotes only. A buy pays the ask plus
// slippage, a sale receives the bid less slippage, every contract pays the
// fee each way, and an idea the quote cannot fill under the rules is recorded
// as NOT_FILLED with the reason rather than dropped. Every book uses the same
// rules, so the books differ only in the ideas.

// expirySettlement is when an expiring contract is settled: the close, 4pm
// New York, approximated as 21:00 UTC.
const expirySettlement = 21 * time.Hour

// OpenShadow simulates buying a contract for book at the snapshot's quote.
// limit is the most the idea would pay per share; 0 means "at the market".
func OpenShadow(book leapmuxv1.TradingBook, snap *Snapshot, occ string, limit float64, rules *leapmuxv1.TradingSimulationRules) *leapmuxv1.TradingPosition {
	pos := &leapmuxv1.TradingPosition{Book: book, OccSymbol: occ, OpenedAtMs: snap.AsOf.UnixMilli()}
	if p, err := ParseOCC(occ); err == nil {
		pos.Underlying, pos.Right, pos.Strike, pos.Expiration = p.Root, p.Right, p.Strike, p.Expiration
	}
	notFilled := func(format string, args ...any) *leapmuxv1.TradingPosition {
		pos.State, pos.Note = leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_NOT_FILLED, fmt.Sprintf(format, args...)
		return pos
	}
	q := snap.Contract(occ)
	if q == nil {
		return notFilled("%s is not in the snapshot", occ)
	}
	pos.Underlying = q.Underlying
	if problems := executionProblems(q, rules); len(problems) > 0 {
		return notFilled("%s", problems[0])
	}
	price := round2(q.Ask + slip(rules))
	if limit > 0 && limit+1e-9 < price {
		return notFilled("the limit %.2f is below the executable price %.2f", limit, price)
	}
	perContract := price*float64(max(q.Multiplier, 100)) + fee(rules)
	qty := uint32(math.Floor(rules.GetAllocationPerIdea() / perContract))
	if qty == 0 {
		return notFilled("one contract costs %.2f, more than the %.2f allocated per idea", perContract, rules.GetAllocationPerIdea())
	}
	pos.State = leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN
	pos.Quantity = qty
	pos.EntryPrice = price
	pos.Fees = round2(fee(rules) * float64(qty))
	pos.Cost = round2(perContract * float64(qty))
	pos.LastMark, pos.LastMarkAtMs = liquidationValue(q.Bid, qty, rules), snap.AsOf.UnixMilli()
	pos.PeakMultiple = grossMultiple(q.Bid, pos)
	return pos
}

// InverseContract picks the opposite-direction contract for an idea: the
// other right on the same underlying and expiry, with the absolute delta
// nearest the original's, or the strike mirrored around the underlying when
// the snapshot has no greeks. It is chosen and priced from the snapshot on its
// own, never derived from the original's price.
func InverseContract(snap *Snapshot, occ string) (*OptionQuote, error) {
	orig := snap.Contract(occ)
	if orig == nil {
		return nil, fmt.Errorf("%s is not in the snapshot", occ)
	}
	other := "put"
	if orig.Right == "put" {
		other = "call"
	}
	ref := 0.0
	if u := snap.Underlying(orig.Underlying); u != nil {
		ref = u.Last
		if u.Bid > 0 && u.Ask >= u.Bid {
			ref = (u.Bid + u.Ask) / 2
		}
	}
	useDelta := orig.Delta != 0
	target := math.Abs(orig.Delta)
	mirror := 2*ref - orig.Strike
	var best *OptionQuote
	bestDist := math.Inf(1)
	for i := range snap.Options {
		c := &snap.Options[i]
		if c.Underlying != orig.Underlying || c.Expiration != orig.Expiration || c.Right != other {
			continue
		}
		dist := math.Abs(c.Strike - mirror)
		if useDelta && c.Delta != 0 {
			dist = math.Abs(math.Abs(c.Delta) - target)
		}
		if dist < bestDist || (dist == bestDist && best != nil && c.OCCSymbol < best.OCCSymbol) {
			best, bestDist = c, dist
		}
	}
	if best == nil {
		return nil, fmt.Errorf("the snapshot has no %s on %s expiring %s", other, orig.Underlying, orig.Expiration)
	}
	return best, nil
}

// liquidationValue is what selling qty contracts at bid would net.
func liquidationValue(bid float64, qty uint32, rules *leapmuxv1.TradingSimulationRules) float64 {
	px := math.Max(bid-slip(rules), 0)
	if px == 0 {
		return 0
	}
	return round2(float64(qty) * (px*100 - fee(rules)))
}

// grossMultiple is the bid value of the position over what it cost.
func grossMultiple(bid float64, pos *leapmuxv1.TradingPosition) float64 {
	if pos.GetCost() <= 0 {
		return 0
	}
	return bid * 100 * float64(pos.GetQuantity()) / pos.GetCost()
}

// Mark is one observation of a position.
type Mark struct {
	AtMs            int64
	Bid             float64
	UnderlyingPrice float64
	Value           float64
	Missing         bool
	HorizonDays     uint32
}

// MarkPosition applies one frame to an open position: it updates the last
// value and the peak, records every horizon the frame passes, and settles the
// position at expiry. It returns the marks to store. A frame without a bid
// records a missing observation and never moves the position.
func MarkPosition(pos *leapmuxv1.TradingPosition, f *Frame, rules *leapmuxv1.TradingSimulationRules) []Mark {
	if pos.GetState() != leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
		return nil
	}
	at := f.AsOf.UTC()
	if at.UnixMilli() <= pos.GetLastMarkAtMs() {
		return nil
	}
	var bid, under float64
	haveBid, haveUnder := false, false
	for _, o := range f.Options {
		if occ, err := NormalizeOCC(o.OCCSymbol); err == nil && occ == pos.GetOccSymbol() && o.Bid > 0 {
			bid, haveBid = o.Bid, true
		}
	}
	for _, u := range f.Underlyings {
		if u.Symbol == pos.GetUnderlying() && u.Last > 0 {
			under, haveUnder = u.Last, true
		}
	}
	var marks []Mark
	exp, _ := time.Parse(time.DateOnly, pos.GetExpiration())
	settleAt := exp.Add(expirySettlement)
	if !at.Before(settleAt) {
		if !haveUnder {
			return []Mark{{AtMs: at.UnixMilli(), Missing: true}}
		}
		intrinsic := math.Max(under-pos.GetStrike(), 0)
		if pos.GetRight() == "put" {
			intrinsic = math.Max(pos.GetStrike()-under, 0)
		}
		value := 0.0
		if intrinsic > 0 {
			value = round2(float64(pos.GetQuantity()) * (intrinsic*100 - fee(rules)))
		}
		marks = append(marks, horizonMarks(pos, settleAt.UnixMilli(), value, rules, true)...)
		pos.State = leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_EXPIRED
		pos.ExitPrice = round2(intrinsic)
		pos.RealizedPnl = round2(value - pos.GetCost())
		pos.LastMark, pos.LastMarkAtMs = value, settleAt.UnixMilli()
		pos.ClosedAtMs = settleAt.UnixMilli()
		pos.PeakMultiple = math.Max(pos.GetPeakMultiple(), intrinsic*100*float64(pos.GetQuantity())/math.Max(pos.GetCost(), 1e-9))
		pos.Note = fmt.Sprintf("Settled at expiry with %s at %.2f", pos.GetUnderlying(), under)
		return append(marks, Mark{AtMs: settleAt.UnixMilli(), UnderlyingPrice: under, Value: value})
	}
	if !haveBid {
		return []Mark{{AtMs: at.UnixMilli(), UnderlyingPrice: under, Missing: true}}
	}
	value := liquidationValue(bid, pos.GetQuantity(), rules)
	marks = append(marks, horizonMarks(pos, at.UnixMilli(), value, rules, false)...)
	pos.LastMark, pos.LastMarkAtMs = value, at.UnixMilli()
	pos.PeakMultiple = math.Max(pos.GetPeakMultiple(), grossMultiple(bid, pos))
	return append(marks, Mark{AtMs: at.UnixMilli(), Bid: bid, UnderlyingPrice: under, Value: value})
}

// horizonMarks records each fixed horizon a mark at atMs reaches for the
// first time, valued at that mark. A final mark (settlement) also fills every
// later horizon with the settled value, since the position cannot change.
func horizonMarks(pos *leapmuxv1.TradingPosition, atMs int64, value float64, rules *leapmuxv1.TradingSimulationRules, final bool) []Mark {
	var out []Mark
	have := map[uint32]bool{}
	for _, h := range pos.GetHorizons() {
		have[h.GetDays()] = true
	}
	for _, d := range rules.GetHorizonDays() {
		due := pos.GetOpenedAtMs() + int64(d)*24*int64(time.Hour/time.Millisecond)
		if have[d] || (atMs < due && !final) {
			continue
		}
		pos.Horizons = append(pos.Horizons, &leapmuxv1.TradingHorizonMark{Days: d, AtMs: atMs, Value: value, Pnl: round2(value - pos.GetCost())})
		out = append(out, Mark{AtMs: atMs, Value: value, HorizonDays: d})
	}
	slices.SortFunc(pos.Horizons, func(a, b *leapmuxv1.TradingHorizonMark) int { return int(a.GetDays()) - int(b.GetDays()) })
	return out
}

// positionPnl is a position's profit so far: realized when closed, marked
// to the bid otherwise.
func positionPnl(p *leapmuxv1.TradingPosition) float64 {
	switch p.GetState() {
	case leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_CLOSED, leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_EXPIRED:
		return p.GetRealizedPnl()
	case leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN:
		return p.GetLastMark() - p.GetCost()
	default:
		return 0
	}
}

// BookMetrics summarizes one book. marks are every position's stored marks,
// for the equity curve.
func BookMetrics(book leapmuxv1.TradingBook, positions []*leapmuxv1.TradingPosition, marks map[string][]Mark, rules *leapmuxv1.TradingSimulationRules) *leapmuxv1.TradingBookMetrics {
	m := &leapmuxv1.TradingBookMetrics{Book: book, Capital: rules.GetStartingCapital(), PnlAtHorizon: map[uint32]float64{}}
	var returns []float64
	var brier []float64
	wins, decided, hit3, hit5 := 0, 0, 0, 0
	for _, p := range positions {
		if p.GetBook() != book {
			continue
		}
		m.Ideas++
		if p.GetState() == leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_NOT_FILLED {
			m.NotExecutable++
			continue
		}
		m.Filled++
		if p.GetState() == leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
			m.Open++
		}
		pnl := positionPnl(p)
		m.RiskDeployed += p.GetCost()
		m.NetPnl += pnl
		if p.GetCost() > 0 {
			returns = append(returns, pnl/p.GetCost())
		}
		if p.GetPeakMultiple() >= 3 {
			hit3++
		}
		if p.GetPeakMultiple() >= 5 {
			hit5++
		}
		if p.GetState() != leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_OPEN {
			decided++
			outcome := 0.0
			if pnl > 0 {
				wins++
				outcome = 1
			}
			if f := p.GetForecastProbability(); f > 0 {
				brier = append(brier, (f-outcome)*(f-outcome))
			}
		}
		for _, h := range p.GetHorizons() {
			m.PnlAtHorizon[h.GetDays()] += h.GetPnl()
		}
	}
	m.RiskDeployed, m.NetPnl = round2(m.RiskDeployed), round2(m.NetPnl)
	if m.RiskDeployed > 0 {
		m.ReturnOnRisk = round4(m.NetPnl / m.RiskDeployed)
	}
	if n := len(returns); n > 1 {
		mean, sd := meanStd(returns)
		if sd > 0 {
			m.RiskAdjustedReturn = round4(mean / sd)
		}
	}
	if decided > 0 {
		m.WinRate = round4(float64(wins) / float64(decided))
	}
	if len(brier) > 0 {
		mean, _ := meanStd(brier)
		m.BrierScore = round4(mean)
	}
	if m.Filled > 0 {
		m.Hit_3XRate = round4(float64(hit3) / float64(m.Filled))
		m.Hit_5XRate = round4(float64(hit5) / float64(m.Filled))
	}
	for d, v := range m.PnlAtHorizon {
		m.PnlAtHorizon[d] = round2(v)
	}
	m.MaxDrawdown = round2(maxDrawdown(book, positions, marks, rules.GetStartingCapital()))
	return m
}

// maxDrawdown walks the book's equity through every recorded mark: each
// position counts at its cost until its first mark and at its latest mark
// after that.
func maxDrawdown(book leapmuxv1.TradingBook, positions []*leapmuxv1.TradingPosition, marks map[string][]Mark, capital float64) float64 {
	type event struct {
		at    int64
		id    string
		value float64
	}
	var events []event
	cost := map[string]float64{}
	for _, p := range positions {
		if p.GetBook() != book || p.GetState() == leapmuxv1.TradingPositionState_TRADING_POSITION_STATE_NOT_FILLED {
			continue
		}
		cost[p.GetId()] = p.GetCost()
		events = append(events, event{at: p.GetOpenedAtMs(), id: p.GetId(), value: p.GetCost() - p.GetFees()})
		for _, mk := range marks[p.GetId()] {
			if !mk.Missing && mk.HorizonDays == 0 {
				events = append(events, event{at: mk.AtMs, id: p.GetId(), value: mk.Value})
			}
		}
	}
	slices.SortStableFunc(events, func(a, b event) int {
		switch {
		case a.at < b.at:
			return -1
		case a.at > b.at:
			return 1
		}
		return 0
	})
	current := map[string]float64{}
	peak, worst := capital, 0.0
	for i, ev := range events {
		current[ev.id] = ev.value
		if i+1 < len(events) && events[i+1].at == ev.at {
			continue
		}
		equity := capital
		for id, v := range current {
			equity += v - cost[id]
		}
		peak = math.Max(peak, equity)
		worst = math.Max(worst, peak-equity)
	}
	return worst
}

func meanStd(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	if len(xs) < 2 {
		return mean, 0
	}
	v := 0.0
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(v / float64(len(xs)-1))
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
func round4(x float64) float64 { return math.Round(x*10000) / 10000 }
