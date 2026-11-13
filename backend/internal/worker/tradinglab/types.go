// Package tradinglab is the privileged half of the blind trading research
// workflow: market snapshots, the Robinhood MCP adapter, the checks and
// scoring of frozen recommendations, the shadow books and the execution
// gateway. The research itself runs on the workflow engine; README.md says
// how the two fit.
package tradinglab

import "time"

// SchemaVersion is the version of every document this package writes.
const SchemaVersion = 1

// UnderlyingQuote is one stock or ETF quote.
type UnderlyingQuote struct {
	Symbol    string    `json:"symbol"`
	Last      float64   `json:"last"`
	Bid       float64   `json:"bid"`
	Ask       float64   `json:"ask"`
	PrevClose float64   `json:"prev_close,omitempty"`
	Volume    int64     `json:"volume,omitempty"`
	QuoteTime time.Time `json:"quote_time"`
}

// OptionQuote is one listed contract with its quote, greeks and size.
// Prices are per share.
type OptionQuote struct {
	OCCSymbol    string    `json:"occ_symbol"`
	Underlying   string    `json:"underlying"`
	Expiration   string    `json:"expiration"` // YYYY-MM-DD
	Strike       float64   `json:"strike"`
	Right        string    `json:"right"` // call | put
	Multiplier   int       `json:"multiplier"`
	Bid          float64   `json:"bid"`
	Ask          float64   `json:"ask"`
	Last         float64   `json:"last,omitempty"`
	IV           float64   `json:"iv,omitempty"`
	Delta        float64   `json:"delta,omitempty"`
	Gamma        float64   `json:"gamma,omitempty"`
	Theta        float64   `json:"theta,omitempty"`
	Vega         float64   `json:"vega,omitempty"`
	OpenInterest int64     `json:"open_interest"`
	Volume       int64     `json:"volume"`
	Tradable     bool      `json:"tradable"`
	QuoteTime    time.Time `json:"quote_time"`
}

// Catalyst is a dated event or a news item about an underlying.
type Catalyst struct {
	Symbol      string    `json:"symbol"`
	Type        string    `json:"type"` // earnings, dividend, split, economic_data, news, ...
	Date        string    `json:"date,omitempty"`
	Title       string    `json:"title"`
	Source      string    `json:"source,omitempty"`
	URL         string    `json:"url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitzero"`
}

// Frame is the market as one provider saw it at one time.
type Frame struct {
	AsOf        time.Time         `json:"as_of"`
	Underlyings []UnderlyingQuote `json:"underlyings"`
	Options     []OptionQuote     `json:"options"`
	Catalysts   []Catalyst        `json:"catalysts,omitempty"`
}

// Provenance records where a snapshot came from.
type Provenance struct {
	Provider   string    `json:"provider"`
	Server     string    `json:"server,omitempty"`
	Tools      []string  `json:"tools,omitempty"`
	Files      []string  `json:"files,omitempty"`
	Collector  string    `json:"collector"`
	CapturedAt time.Time `json:"captured_at"`
}

// Freshness states the age rules a snapshot was checked against.
type Freshness struct {
	MaxQuoteAgeSeconds int      `json:"max_quote_age_seconds"`
	StaleQuotes        []string `json:"stale_quotes,omitempty"`
	MissingQuotes      []string `json:"missing_quotes,omitempty"`
}

// Snapshot is the immutable market input both analysts receive. Its hash is
// the workflow's frozen-output hash of its canonical JSON.
type Snapshot struct {
	SchemaVersion int               `json:"schema_version"`
	AsOf          time.Time         `json:"as_of"`
	Source        string            `json:"source"`
	Provenance    Provenance        `json:"provenance"`
	Freshness     Freshness         `json:"freshness"`
	Rules         SnapshotRules     `json:"rules"`
	Underlyings   []UnderlyingQuote `json:"underlyings"`
	Options       []OptionQuote     `json:"options"`
	Catalysts     []Catalyst        `json:"catalysts"`
	Warnings      []string          `json:"warnings"`
}

// SnapshotRules is what a snapshot was cut to: the contracts an analyst may
// choose among.
type SnapshotRules struct {
	Symbols        []string `json:"symbols"`
	MaxDTE         int      `json:"max_days_to_expiry"`
	StrikesPerSide int      `json:"strikes_per_side"`
}

// Contract looks up an option by OCC symbol.
func (s *Snapshot) Contract(occ string) *OptionQuote {
	for i := range s.Options {
		if s.Options[i].OCCSymbol == occ {
			return &s.Options[i]
		}
	}
	return nil
}

// Underlying looks up an underlying quote.
func (s *Snapshot) Underlying(symbol string) *UnderlyingQuote {
	for i := range s.Underlyings {
		if s.Underlyings[i].Symbol == symbol {
			return &s.Underlyings[i]
		}
	}
	return nil
}

// --- Research documents (schemas/*.schema.json) ---

// Prediction is one analyst's frozen output.
type Prediction struct {
	SchemaVersion   int              `json:"schema_version"`
	SnapshotSHA256  string           `json:"snapshot_sha256"`
	NoTrade         bool             `json:"no_trade"`
	NoTradeReason   string           `json:"no_trade_reason,omitempty"`
	MarketView      string           `json:"market_view,omitempty"`
	Recommendations []Recommendation `json:"recommendations"`
}

type Recommendation struct {
	ID         string `json:"id"`
	Underlying string `json:"underlying"`
	Direction  string `json:"direction"`
	Strategy   string `json:"strategy"`
	Contract   struct {
		OCCSymbol  string  `json:"occ_symbol"`
		Expiration string  `json:"expiration"`
		Strike     float64 `json:"strike"`
		Right      string  `json:"right"`
	} `json:"contract"`
	Entry struct {
		LimitPrice float64 `json:"limit_price"`
		Rationale  string  `json:"rationale,omitempty"`
	} `json:"entry"`
	Thesis   string `json:"thesis"`
	Catalyst struct {
		Type        string `json:"type"`
		Date        string `json:"date,omitempty"`
		Description string `json:"description"`
	} `json:"catalyst"`
	Evidence []struct {
		Claim  string `json:"claim"`
		Source string `json:"source"`
	} `json:"evidence"`
	Scenarios []struct {
		Name            string  `json:"name"`
		Probability     float64 `json:"probability"`
		UnderlyingPrice float64 `json:"underlying_price"`
		OptionValue     float64 `json:"option_value"`
	} `json:"scenarios"`
	Probabilities struct {
		Profit  float64 `json:"profit"`
		Reach3x float64 `json:"reach_3x"`
		Reach5x float64 `json:"reach_5x"`
	} `json:"probabilities"`
	Risk struct {
		MaxLossPerContract float64 `json:"max_loss_per_contract"`
		Notes              string  `json:"notes,omitempty"`
	} `json:"risk"`
	Exits struct {
		TakeProfit []struct {
			Price    float64 `json:"price"`
			Fraction float64 `json:"fraction"`
		} `json:"take_profit"`
		StopLoss float64 `json:"stop_loss"`
		TimeStop string  `json:"time_stop"`
	} `json:"exits"`
	Invalidation []string `json:"invalidation"`
	HorizonDays  int      `json:"horizon_days"`
	Confidence   float64  `json:"confidence,omitempty"`
}

// Evaluation is the evaluator's frozen output.
type Evaluation struct {
	SchemaVersion int    `json:"schema_version"`
	RubricVersion string `json:"rubric_version"`
	Submissions   []struct {
		Label             string `json:"label"`
		NoTradeAssessment string `json:"no_trade_assessment,omitempty"`
		Recommendations   []struct {
			RecommendationID string         `json:"recommendation_id"`
			Scores           map[string]int `json:"scores"`
			Verdict          string         `json:"verdict"`
			Notes            string         `json:"notes"`
		} `json:"recommendations"`
	} `json:"submissions"`
}

// Audit is the risk auditor's frozen output.
type Audit struct {
	SchemaVersion int `json:"schema_version"`
	Findings      []struct {
		Label                      string          `json:"label"`
		RecommendationID           string          `json:"recommendation_id"`
		Verdict                    string          `json:"verdict"`
		Checks                     map[string]bool `json:"checks"`
		Issues                     []string        `json:"issues"`
		ComputedMaxLossPerContract float64         `json:"computed_max_loss_per_contract"`
	} `json:"findings"`
}
