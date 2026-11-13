package tradinglab

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// SampleSymbols are the underlyings of the synthetic recording.
var SampleSymbols = []string{"SYNA", "SYNB"}

// GenerateSampleReplay writes a synthetic market recording: one frame per
// weekday at 15:30 New York (20:30 UTC) for days weekdays from start, with
// Black-Scholes-priced option chains, greeks and a dated earnings event.
// Every value is invented and every file says so in its name, so a paper lab
// works with no credentials and nobody mistakes the data for a market's. The
// same seed always writes the same files.
func GenerateSampleReplay(dir string, start time.Time, days int, seed uint64) ([]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	price := map[string]float64{"SYNA": 100, "SYNB": 42}
	vol := map[string]float64{"SYNA": 0.45, "SYNB": 0.8}
	day := time.Date(start.Year(), start.Month(), start.Day(), 20, 30, 0, 0, time.UTC)
	earnings := map[string]string{}
	var written []string
	for n := 0; n < days; {
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			day = day.AddDate(0, 0, 1)
			continue
		}
		f := &Frame{AsOf: day}
		for _, sym := range SampleSymbols {
			if n > 0 {
				shock := rng.NormFloat64() * vol[sym] / math.Sqrt(252)
				price[sym] = round2(price[sym] * math.Exp(shock))
			}
			if earnings[sym] == "" {
				earnings[sym] = day.AddDate(0, 0, 4+len(sym)%3).Format(time.DateOnly)
			}
			p := price[sym]
			f.Underlyings = append(f.Underlyings, UnderlyingQuote{
				Symbol: sym, Last: p, Bid: round2(p - 0.02), Ask: round2(p + 0.02), Volume: int64(1_000_000 + rng.IntN(500_000)), QuoteTime: day,
			})
			for _, exp := range nextFridays(day, 3) {
				t := math.Max(exp.Add(expirySettlement).Sub(day).Hours()/24/365, 1.0/365/4)
				step := strikeStep(p)
				atm := math.Round(p/step) * step
				for k := -8; k <= 8; k++ {
					strike := round2(atm + float64(k)*step)
					if strike <= 0 {
						continue
					}
					for _, right := range []string{"call", "put"} {
						occ, _ := OCCSymbol(sym, exp.Format(time.DateOnly), right, strike)
						iv := vol[sym] * (1 + 0.15*math.Abs(math.Log(strike/p))*10)
						fair, delta, gamma, theta, vega := blackScholes(right, p, strike, t, iv)
						half := math.Max(0.01, math.Min(0.15, fair*0.04))
						bid := round2(math.Max(fair-half, 0))
						ask := round2(math.Max(fair+half, 0.01))
						f.Options = append(f.Options, OptionQuote{
							OCCSymbol: occ, Underlying: sym, Expiration: exp.Format(time.DateOnly), Strike: strike, Right: right, Multiplier: 100,
							Bid: bid, Ask: ask, Last: round2(fair), IV: round4(iv), Delta: round4(delta), Gamma: round4(gamma), Theta: round4(theta), Vega: round4(vega),
							OpenInterest: int64(200 + rng.IntN(4000) - 150*int(math.Abs(float64(k)))), Volume: int64(20 + rng.IntN(900)), Tradable: true, QuoteTime: day,
						})
					}
				}
			}
			f.Catalysts = append(f.Catalysts, Catalyst{
				Symbol: sym, Type: "earnings", Date: earnings[sym], Title: sym + " reports quarterly earnings (synthetic)", Source: "synthetic",
			})
		}
		name := fmt.Sprintf("synthetic-%s.json", day.Format("20060102T1504Z"))
		raw, err := json.MarshalIndent(f, "", " ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			return nil, err
		}
		written = append(written, name)
		day = day.AddDate(0, 0, 1)
		n++
	}
	return written, nil
}

func nextFridays(from time.Time, n int) []time.Time {
	var out []time.Time
	d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	for len(out) < n {
		if d.Weekday() == time.Friday {
			out = append(out, d)
		}
		d = d.AddDate(0, 0, 1)
	}
	return out
}

func strikeStep(p float64) float64 {
	switch {
	case p < 25:
		return 0.5
	case p < 200:
		return 1
	default:
		return 5
	}
}

// blackScholes prices a European option with no rates or dividends, which is
// all invented data needs: the price, delta, gamma, theta per day and vega
// per volatility point.
func blackScholes(right string, s, k, t, iv float64) (price, delta, gamma, theta, vega float64) {
	sq := iv * math.Sqrt(t)
	d1 := (math.Log(s/k) + 0.5*iv*iv*t) / sq
	d2 := d1 - sq
	pdf := math.Exp(-d1*d1/2) / math.Sqrt(2*math.Pi)
	gamma = pdf / (s * sq)
	vega = s * pdf * math.Sqrt(t) / 100
	theta = -s * pdf * iv / (2 * math.Sqrt(t)) / 365
	if right == "call" {
		return s*cdf(d1) - k*cdf(d2), cdf(d1), gamma, theta, vega
	}
	return k*cdf(-d2) - s*cdf(-d1), cdf(d1) - 1, gamma, theta, vega
}

func cdf(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// InstallSampleData writes a fresh synthetic recording under the worker's
// data directory, ending today, and points the user's replay at it.
func (l *Lab) InstallSampleData(ctx context.Context, userID, dataDir string) (*leapmuxv1.InstallTradingSampleDataResponse, error) {
	now := l.now()
	dir, err := filepath.Abs(filepath.Join(dataDir, "tradinglab", "synthetic-replay", now.Format("20060102T150405")))
	if err != nil {
		return nil, err
	}
	files, err := GenerateSampleReplay(dir, now.AddDate(0, 0, -21), 15, uint64(now.UnixNano()))
	if err != nil {
		return nil, err
	}
	s, err := l.Settings(ctx, userID)
	if err != nil {
		return nil, err
	}
	s.MarketData, s.ReplayDir, s.Watchlist = leapmuxv1.TradingMarketDataSource_TRADING_MARKET_DATA_SOURCE_REPLAY, dir, SampleSymbols
	saved, err := l.SaveSettings(ctx, userID, &leapmuxv1.SaveTradingLabSettingsRequest{Settings: s})
	if err != nil {
		return nil, err
	}
	return &leapmuxv1.InstallTradingSampleDataResponse{Settings: saved, Files: files}, nil
}
