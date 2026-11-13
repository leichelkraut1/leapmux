package tradinglab

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/workflow"
)

// collectorVersion names this code in every snapshot's provenance.
const collectorVersion = "leapmux-tradinglab/1"

// SnapshotOptions are the cut and the freshness rules of one capture.
type SnapshotOptions struct {
	Symbols        []string
	MaxDTE         int
	StrikesPerSide int
	// MaxQuoteAge drops a quote older than this at the frame's time.
	MaxQuoteAge time.Duration
	// MaxSnapshotAge refuses a frame older than this at Now.
	MaxSnapshotAge time.Duration
	// Now is the present the frame is judged against: the wall clock for a
	// live provider, the recording's own time for a replay.
	Now time.Time
}

// ErrStale is a frame too old, or too empty, to research.
var ErrStale = errors.New("market data failed its freshness check")

// BuildSnapshot cuts a frame to the requested underlyings and contracts,
// drops every quote that is stale, missing or inconsistent (recording each),
// and orders everything deterministically, so one market yields one hash.
func BuildSnapshot(f *Frame, source string, prov Provenance, o SnapshotOptions) (*Snapshot, error) {
	if f == nil || f.AsOf.IsZero() {
		return nil, fmt.Errorf("%w: the frame has no time", ErrStale)
	}
	asOf := f.AsOf.UTC()
	if o.MaxSnapshotAge > 0 && o.Now.Sub(asOf) > o.MaxSnapshotAge {
		return nil, fmt.Errorf("%w: the market data is from %s, older than %s", ErrStale, asOf.Format(time.RFC3339), o.MaxSnapshotAge)
	}
	if o.Now.Before(asOf.Add(-time.Minute)) {
		return nil, fmt.Errorf("%w: the market data claims a time after now", ErrStale)
	}
	want := map[string]bool{}
	symbols := make([]string, 0, len(o.Symbols))
	for _, s := range o.Symbols {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s != "" && !want[s] {
			want[s] = true
			symbols = append(symbols, s)
		}
	}
	slices.Sort(symbols)
	if len(symbols) == 0 {
		return nil, errors.New("the snapshot names no underlyings")
	}
	snap := &Snapshot{
		SchemaVersion: SchemaVersion, AsOf: asOf, Source: source, Provenance: prov,
		Freshness:   Freshness{MaxQuoteAgeSeconds: int(o.MaxQuoteAge / time.Second)},
		Rules:       SnapshotRules{Symbols: symbols, MaxDTE: o.MaxDTE, StrikesPerSide: o.StrikesPerSide},
		Underlyings: []UnderlyingQuote{}, Options: []OptionQuote{}, Catalysts: []Catalyst{}, Warnings: []string{},
	}
	snap.Provenance.Collector = collectorVersion
	stale := func(t time.Time) bool {
		return t.IsZero() || (o.MaxQuoteAge > 0 && asOf.Sub(t) > o.MaxQuoteAge)
	}

	last := map[string]float64{}
	for _, u := range f.Underlyings {
		u.Symbol = strings.ToUpper(strings.TrimSpace(u.Symbol))
		if !want[u.Symbol] {
			continue
		}
		u.QuoteTime = u.QuoteTime.UTC()
		switch {
		case u.Last <= 0 && (u.Bid <= 0 || u.Ask <= 0):
			snap.Freshness.MissingQuotes = append(snap.Freshness.MissingQuotes, u.Symbol)
			continue
		case stale(u.QuoteTime):
			snap.Freshness.StaleQuotes = append(snap.Freshness.StaleQuotes, u.Symbol)
			continue
		}
		ref := u.Last
		if u.Bid > 0 && u.Ask >= u.Bid {
			ref = (u.Bid + u.Ask) / 2
		}
		last[u.Symbol] = ref
		snap.Underlyings = append(snap.Underlyings, u)
	}
	for _, s := range symbols {
		if _, ok := last[s]; !ok {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s has no fresh quote, so none of its options are included", s))
		}
	}
	if len(snap.Underlyings) == 0 {
		return nil, fmt.Errorf("%w: no requested underlying has a fresh quote", ErrStale)
	}

	today := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	type chainKey struct{ underlying, expiration, right string }
	chains := map[chainKey][]OptionQuote{}
	for _, q := range f.Options {
		occ, err := NormalizeOCC(q.OCCSymbol)
		if err != nil {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("dropped a contract with an unreadable symbol %q", q.OCCSymbol))
			continue
		}
		parsed, _ := ParseOCC(occ)
		q.OCCSymbol = occ
		q.Underlying = strings.ToUpper(strings.TrimSpace(q.Underlying))
		if q.Underlying == "" {
			q.Underlying = parsed.Root
		}
		if _, ok := last[q.Underlying]; !ok {
			continue
		}
		if q.Expiration != parsed.Expiration || q.Right != parsed.Right || math.Abs(q.Strike-parsed.Strike) > 1e-6 {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("dropped %s: its fields disagree with its symbol", occ))
			continue
		}
		exp, _ := time.Parse(time.DateOnly, q.Expiration)
		dte := int(exp.Sub(today).Hours() / 24)
		if dte < 0 || (o.MaxDTE > 0 && dte > o.MaxDTE) {
			continue
		}
		if q.Multiplier == 0 {
			q.Multiplier = 100
		}
		q.QuoteTime = q.QuoteTime.UTC()
		switch {
		case q.Bid <= 0 && q.Ask <= 0:
			snap.Freshness.MissingQuotes = append(snap.Freshness.MissingQuotes, occ)
			continue
		case q.Ask > 0 && q.Bid > q.Ask:
			snap.Warnings = append(snap.Warnings, fmt.Sprintf("dropped %s: its bid is above its ask", occ))
			continue
		case stale(q.QuoteTime):
			snap.Freshness.StaleQuotes = append(snap.Freshness.StaleQuotes, occ)
			continue
		}
		k := chainKey{q.Underlying, q.Expiration, q.Right}
		chains[k] = append(chains[k], q)
	}
	for k, quotes := range chains {
		ref := last[k.underlying]
		slices.SortFunc(quotes, func(a, b OptionQuote) int {
			da, db := math.Abs(a.Strike-ref), math.Abs(b.Strike-ref)
			if da != db {
				if da < db {
					return -1
				}
				return 1
			}
			return strings.Compare(a.OCCSymbol, b.OCCSymbol)
		})
		keep := quotes
		if o.StrikesPerSide > 0 && len(keep) > 2*o.StrikesPerSide {
			keep = keep[:2*o.StrikesPerSide]
		}
		snap.Options = append(snap.Options, keep...)
	}
	slices.SortFunc(snap.Options, func(a, b OptionQuote) int {
		if c := strings.Compare(a.Underlying, b.Underlying); c != 0 {
			return c
		}
		return strings.Compare(a.OCCSymbol, b.OCCSymbol)
	})
	slices.SortFunc(snap.Underlyings, func(a, b UnderlyingQuote) int { return strings.Compare(a.Symbol, b.Symbol) })
	for _, c := range f.Catalysts {
		c.Symbol = strings.ToUpper(strings.TrimSpace(c.Symbol))
		if want[c.Symbol] || c.Symbol == "" {
			c.PublishedAt = c.PublishedAt.UTC()
			snap.Catalysts = append(snap.Catalysts, c)
		}
	}
	slices.SortFunc(snap.Catalysts, func(a, b Catalyst) int {
		return strings.Compare(a.Symbol+"|"+a.Date+"|"+a.Type+"|"+a.Title, b.Symbol+"|"+b.Date+"|"+b.Type+"|"+b.Title)
	})
	slices.Sort(snap.Freshness.StaleQuotes)
	slices.Sort(snap.Freshness.MissingQuotes)
	if len(snap.Options) == 0 {
		return nil, fmt.Errorf("%w: no option contract with a fresh two-sided quote is within %d days", ErrStale, o.MaxDTE)
	}
	if len(snap.Catalysts) == 0 {
		snap.Warnings = append(snap.Warnings, "no news or corporate events were available for these underlyings")
	}
	return snap, nil
}

// Canonical renders a snapshot in its hashed form.
func (s *Snapshot) Canonical() (string, string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", "", err
	}
	return workflow.CanonicalJSON(string(raw))
}

// DecodeSnapshot reads a frozen snapshot.
func DecodeSnapshot(doc string) (*Snapshot, error) {
	s := &Snapshot{}
	if err := json.Unmarshal([]byte(doc), s); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	return s, nil
}
