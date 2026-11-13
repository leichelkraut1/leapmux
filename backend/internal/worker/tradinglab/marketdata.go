package tradinglab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// MarketData is where snapshots and marks come from. Neither implementation
// can place or change anything.
type MarketData interface {
	Source() leapmuxv1.TradingMarketDataSource
	// Capture returns the market at the provider's present for the symbols,
	// with option chains to maxDTE days, and that present.
	Capture(ctx context.Context, symbols []string, maxDTE int) (*Frame, Provenance, time.Time, error)
	// FramesSince returns the frames after since that cover the contracts,
	// oldest first: every later recording for a replay, one frame captured
	// now for a live source.
	FramesSince(ctx context.Context, since time.Time, contracts, symbols []string) ([]*Frame, error)
}

// --- Replay ---

// Replay serves recorded market files: a directory of JSON Frames, one per
// file. Its present is the newest recording, so a paper run needs no
// credentials and no network, and later recordings mark the books as time
// passes.
type Replay struct {
	Dir string
}

func (r *Replay) Source() leapmuxv1.TradingMarketDataSource {
	return leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_REPLAY
}

type replayFile struct {
	path  string
	frame *Frame
}

func (r *Replay) load() ([]replayFile, error) {
	if strings.TrimSpace(r.Dir) == "" {
		return nil, errors.New("no replay directory is configured")
	}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil, fmt.Errorf("read the replay directory: %w", err)
	}
	var out []replayFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.Dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		f := &Frame{}
		if err := json.Unmarshal(raw, f); err != nil {
			return nil, fmt.Errorf("replay file %s: %w", e.Name(), err)
		}
		if f.AsOf.IsZero() {
			return nil, fmt.Errorf("replay file %s has no as_of", e.Name())
		}
		out = append(out, replayFile{path: e.Name(), frame: f})
	}
	slices.SortFunc(out, func(a, b replayFile) int { return a.frame.AsOf.Compare(b.frame.AsOf) })
	if len(out) == 0 {
		return nil, fmt.Errorf("the replay directory %s holds no market files", r.Dir)
	}
	return out, nil
}

// Capture returns the earliest recording no run has used up: the first one,
// so a fresh paper lab researches the start of the recording and marks
// against the rest. A cursor file records which one each capture took.
func (r *Replay) Capture(_ context.Context, _ []string, _ int) (*Frame, Provenance, time.Time, error) {
	files, err := r.load()
	if err != nil {
		return nil, Provenance{}, time.Time{}, err
	}
	pick := files[0]
	if cursor, err := os.ReadFile(filepath.Join(r.Dir, ".cursor")); err == nil {
		for i, f := range files {
			if f.path == strings.TrimSpace(string(cursor)) && i+1 < len(files) {
				pick = files[i+1]
			}
		}
	}
	_ = os.WriteFile(filepath.Join(r.Dir, ".cursor"), []byte(pick.path), 0o600)
	prov := Provenance{Provider: "replay", Files: []string{pick.path}, CapturedAt: pick.frame.AsOf}
	return pick.frame, prov, pick.frame.AsOf, nil
}

func (r *Replay) FramesSince(_ context.Context, since time.Time, _, _ []string) ([]*Frame, error) {
	files, err := r.load()
	if err != nil {
		return nil, err
	}
	var out []*Frame
	for _, f := range files {
		if f.frame.AsOf.After(since) {
			out = append(out, f.frame)
		}
	}
	return out, nil
}

// --- Live (Robinhood, read-only) ---

// LiveMarket captures through a broker's read-only surface.
type LiveMarket struct {
	Broker ReadOnlyBroker
	Server string
	Now    func() time.Time
}

func (l *LiveMarket) Source() leapmuxv1.TradingMarketDataSource {
	return leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_ROBINHOOD
}

func (l *LiveMarket) Capture(ctx context.Context, symbols []string, maxDTE int) (*Frame, Provenance, time.Time, error) {
	now := l.Now().UTC()
	f := &Frame{AsOf: now}
	quotes, err := l.Broker.Quotes(ctx, symbols)
	if err != nil {
		return nil, Provenance{}, now, fmt.Errorf("underlying quotes: %w", err)
	}
	f.Underlyings = quotes
	tools := []string{OpQuote, OpOptionChain}
	for _, s := range symbols {
		chain, err := l.Broker.OptionChain(ctx, s, maxDTE, now)
		if err != nil {
			return nil, Provenance{}, now, fmt.Errorf("option chain for %s: %w", s, err)
		}
		f.Options = append(f.Options, chain...)
	}
	cats, _ := l.Broker.Catalysts(ctx, symbols)
	f.Catalysts = cats
	for _, op := range []string{OpEvents, OpNews} {
		if l.Broker.Supports(op) {
			tools = append(tools, op)
		}
	}
	return f, Provenance{Provider: "robinhood-mcp", Server: l.Server, Tools: tools, CapturedAt: now}, now, nil
}

func (l *LiveMarket) FramesSince(ctx context.Context, since time.Time, contracts, symbols []string) ([]*Frame, error) {
	now := l.Now().UTC()
	if !now.After(since) {
		return nil, nil
	}
	f := &Frame{AsOf: now}
	if len(symbols) > 0 {
		quotes, err := l.Broker.Quotes(ctx, symbols)
		if err != nil {
			return nil, err
		}
		f.Underlyings = quotes
	}
	if len(contracts) > 0 {
		opts, err := l.Broker.OptionQuotes(ctx, contracts)
		if err != nil {
			return nil, err
		}
		f.Options = opts
	}
	return []*Frame{f}, nil
}
