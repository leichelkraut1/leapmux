package tradinglab

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The output schemas of the research steps, embedded into the bundled
// template so the engine validates exactly what this package reads.
var (
	//go:embed schemas/prediction.schema.json
	PredictionSchema string
	//go:embed schemas/evaluation.schema.json
	EvaluationSchema string
	//go:embed schemas/audit.schema.json
	AuditSchema string
)

// RubricVersion is the scoring rubric the evaluation schema pins.
const RubricVersion = "options-v1"

// rubricWeights is the one rubric every recommendation is totalled by. The
// evaluator states component scores only; the total is this arithmetic, so
// two submissions with the same scores always total the same, whoever
// submitted them and under whatever label.
var rubricWeights = map[string]float64{
	"evidence":      0.20,
	"pricing":       0.20,
	"catalyst":      0.15,
	"risk_reward":   0.20,
	"calibration":   0.10,
	"executability": 0.15,
}

// RubricScore totals component scores (0-5 each) to 0-100. A missing or out
// of range component is an error rather than a zero, so an incomplete
// evaluation cannot pass as a poor one.
func RubricScore(scores map[string]int) (float64, error) {
	total := 0.0
	for name, w := range rubricWeights {
		v, ok := scores[name]
		if !ok {
			return 0, fmt.Errorf("the evaluation has no %s score", name)
		}
		if v < 0 || v > 5 {
			return 0, fmt.Errorf("the %s score %d is outside 0-5", name, v)
		}
		total += w * float64(v)
	}
	for name := range scores {
		if _, ok := rubricWeights[name]; !ok {
			return 0, fmt.Errorf("the evaluation scores %q, which the rubric does not have", name)
		}
	}
	return math.Round(total/5*1000) / 10, nil
}

// fee is the per-contract fee the rules charge.
func fee(rules *leapmuxv1.TradingSimulationRules) float64 { return rules.GetFeePerContract() }

// slip is the per-share slippage the rules add to a buy and take from a sale.
func slip(rules *leapmuxv1.TradingSimulationRules) float64 {
	return rules.GetSlippagePerContract() / 100
}

// CheckRecommendation runs the lab's own checks of one recommendation against
// the snapshot it claims to be based on. Every failure is a stated reason; an
// empty result means the idea is internally consistent and executable at the
// recorded quote. These checks, not the agents' word, decide eligibility.
func CheckRecommendation(snap *Snapshot, snapSHA string, p *Prediction, r *Recommendation, rules *leapmuxv1.TradingSimulationRules) []string {
	var reasons []string
	fail := func(format string, args ...any) { reasons = append(reasons, fmt.Sprintf(format, args...)) }

	if p.SnapshotSHA256 != snapSHA {
		fail("it cites snapshot %s, not the frozen input %s", short(p.SnapshotSHA256), short(snapSHA))
	}
	wantRight, wantDir := "call", "bullish"
	if r.Strategy == "long_put" {
		wantRight, wantDir = "put", "bearish"
	}
	if r.Contract.Right != wantRight || r.Direction != wantDir {
		fail("%s must be a %s %s, not a %s %s", r.Strategy, wantDir, wantRight, r.Direction, r.Contract.Right)
	}
	occ, err := NormalizeOCC(r.Contract.OCCSymbol)
	if err != nil {
		fail("%v", err)
		return reasons
	}
	q := snap.Contract(occ)
	if q == nil {
		fail("%s is not in the snapshot", occ)
		return reasons
	}
	if q.Underlying != r.Underlying || q.Expiration != r.Contract.Expiration || math.Abs(q.Strike-r.Contract.Strike) > 1e-6 || q.Right != r.Contract.Right {
		fail("the contract fields disagree with %s in the snapshot", occ)
	}
	if !q.Tradable {
		fail("%s is not tradable", occ)
	}
	reasons = append(reasons, executionProblems(q, rules)...)
	limit := r.Entry.LimitPrice
	fill := q.Ask + slip(rules)
	if limit+1e-9 < fill {
		fail("the limit %.2f is below the executable price %.2f (ask %.2f plus slippage)", limit, fill, q.Ask)
	}
	if limit > q.Ask*1.25+0.05 {
		fail("the limit %.2f is far above the ask %.2f", limit, q.Ask)
	}
	expected := limit*100 + fee(rules)
	if d := math.Abs(r.Risk.MaxLossPerContract - expected); d > math.Max(1, expected*0.01) {
		fail("it states a maximum loss of %.2f per contract; the limit and fees make it %.2f", r.Risk.MaxLossPerContract, expected)
	}

	exp, _ := time.Parse(time.DateOnly, q.Expiration)
	asOfDay := time.Date(snap.AsOf.Year(), snap.AsOf.Month(), snap.AsOf.Day(), 0, 0, 0, 0, time.UTC)
	dte := int(exp.Sub(asOfDay).Hours() / 24)
	if r.HorizonDays > dte+1 {
		fail("its horizon of %d days outlives the contract (%d days to expiry)", r.HorizonDays, dte)
	}
	if ts, err := time.Parse(time.DateOnly, r.Exits.TimeStop); err != nil || ts.After(exp) || ts.Before(asOfDay) {
		fail("its time stop %q is not between the snapshot date and expiry", r.Exits.TimeStop)
	}
	if r.Catalyst.Date != "" {
		if cd, err := time.Parse(time.DateOnly, r.Catalyst.Date); err != nil || cd.After(exp) || cd.Before(asOfDay) {
			fail("its catalyst date %q is not between the snapshot date and expiry", r.Catalyst.Date)
		}
	}
	sum := 0.0
	for _, s := range r.Scenarios {
		sum += s.Probability
	}
	if math.Abs(sum-1) > 0.05 {
		fail("its scenario probabilities sum to %.2f, not 1", sum)
	}
	if r.Probabilities.Reach5x > r.Probabilities.Reach3x+1e-9 {
		fail("it rates reaching 5x (%.2f) likelier than reaching 3x (%.2f)", r.Probabilities.Reach5x, r.Probabilities.Reach3x)
	}
	fractions := 0.0
	for _, tp := range r.Exits.TakeProfit {
		fractions += tp.Fraction
		if tp.Price <= limit {
			fail("a take-profit price %.2f is not above the entry %.2f", tp.Price, limit)
		}
	}
	if fractions > 1+1e-9 {
		fail("its take-profit fractions sum to %.2f, more than the whole position", fractions)
	}
	if r.Exits.StopLoss >= limit {
		fail("its stop %.2f is not below the entry %.2f", r.Exits.StopLoss, limit)
	}
	return reasons
}

// executionProblems is why a quote cannot be traded under the rules.
func executionProblems(q *OptionQuote, rules *leapmuxv1.TradingSimulationRules) []string {
	var out []string
	if q.Ask <= 0 || q.Bid <= 0 {
		out = append(out, fmt.Sprintf("%s has no two-sided quote (bid %.2f, ask %.2f)", q.OCCSymbol, q.Bid, q.Ask))
		return out
	}
	if max := rules.GetMaxSpreadFraction(); max > 0 && (q.Ask-q.Bid)/q.Ask > max {
		out = append(out, fmt.Sprintf("%s's spread %.2f-%.2f is wider than %.0f%% of the ask", q.OCCSymbol, q.Bid, q.Ask, max*100))
	}
	if q.OpenInterest < int64(rules.GetMinOpenInterest()) {
		out = append(out, fmt.Sprintf("%s's open interest %d is below %d", q.OCCSymbol, q.OpenInterest, rules.GetMinOpenInterest()))
	}
	if q.Volume < int64(rules.GetMinVolume()) {
		out = append(out, fmt.Sprintf("%s's volume %d is below %d", q.OCCSymbol, q.Volume, rules.GetMinVolume()))
	}
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// DecodePrediction reads a frozen prediction.
func DecodePrediction(doc string) (*Prediction, error) {
	p := &Prediction{}
	if err := json.Unmarshal([]byte(doc), p); err != nil {
		return nil, fmt.Errorf("decode prediction: %w", err)
	}
	return p, nil
}

// EvaluationScores maps "label/recommendation id" to its rubric total and
// verdict. It reports every submission recommendation the evaluation skipped
// or invented, so an incomplete evaluation is visible.
func EvaluationScores(e *Evaluation, submissions map[string]*Prediction) (map[string]float64, map[string]string, []string) {
	scores, verdicts := map[string]float64{}, map[string]string{}
	var problems []string
	seen := map[string]bool{}
	for _, s := range e.Submissions {
		p := submissions[s.Label]
		if p == nil {
			problems = append(problems, fmt.Sprintf("the evaluation scores %s, which was not submitted", s.Label))
			continue
		}
		for _, r := range s.Recommendations {
			key := s.Label + "/" + r.RecommendationID
			if !slices.ContainsFunc(p.Recommendations, func(x Recommendation) bool { return x.ID == r.RecommendationID }) {
				problems = append(problems, fmt.Sprintf("the evaluation scores %s, which %s does not contain", r.RecommendationID, s.Label))
				continue
			}
			total, err := RubricScore(r.Scores)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", key, err))
				continue
			}
			scores[key], verdicts[key], seen[key] = total, r.Verdict, true
		}
	}
	for label, p := range submissions {
		for _, r := range p.Recommendations {
			if key := label + "/" + r.ID; !seen[key] {
				problems = append(problems, fmt.Sprintf("the evaluation did not score %s", key))
			}
		}
	}
	slices.Sort(problems)
	return scores, verdicts, problems
}
