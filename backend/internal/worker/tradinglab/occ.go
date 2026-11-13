package tradinglab

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// OCCSymbol renders the OSI/OCC option symbol: the root padded to six
// characters, YYMMDD, C or P, and the strike times 1000 in eight digits.
func OCCSymbol(root, expiration string, right string, strike float64) (string, error) {
	exp, err := time.Parse(time.DateOnly, expiration)
	if err != nil {
		return "", fmt.Errorf("expiration %q is not YYYY-MM-DD", expiration)
	}
	root = strings.ToUpper(strings.TrimSpace(root))
	if root == "" || len(root) > 6 {
		return "", fmt.Errorf("root %q must be 1 to 6 characters", root)
	}
	var cp string
	switch right {
	case "call":
		cp = "C"
	case "put":
		cp = "P"
	default:
		return "", fmt.Errorf("right %q is not call or put", right)
	}
	milli := math.Round(strike * 1000)
	if milli <= 0 || milli > 99999999 {
		return "", fmt.Errorf("strike %v is out of range", strike)
	}
	return fmt.Sprintf("%-6s%s%s%08d", root, exp.Format("060102"), cp, int64(milli)), nil
}

// ParsedOCC is an OCC symbol's parts.
type ParsedOCC struct {
	Root       string
	Expiration string
	Right      string
	Strike     float64
}

// ParseOCC reads an OCC symbol, padded or not ("XYZ261120C00110000").
func ParseOCC(sym string) (ParsedOCC, error) {
	s := strings.TrimSpace(sym)
	if len(s) < 16 {
		return ParsedOCC{}, fmt.Errorf("%q is too short for an OCC symbol", sym)
	}
	tail := s[len(s)-15:]
	root := strings.TrimSpace(s[:len(s)-15])
	if root == "" || len(root) > 6 {
		return ParsedOCC{}, fmt.Errorf("%q has no valid root", sym)
	}
	exp, err := time.Parse("060102", tail[:6])
	if err != nil {
		return ParsedOCC{}, fmt.Errorf("%q has no valid expiration", sym)
	}
	var right string
	switch tail[6] {
	case 'C':
		right = "call"
	case 'P':
		right = "put"
	default:
		return ParsedOCC{}, fmt.Errorf("%q is neither a call nor a put", sym)
	}
	milli, err := strconv.ParseInt(tail[7:], 10, 64)
	if err != nil || milli <= 0 {
		return ParsedOCC{}, fmt.Errorf("%q has no valid strike", sym)
	}
	return ParsedOCC{Root: root, Expiration: exp.Format(time.DateOnly), Right: right, Strike: float64(milli) / 1000}, nil
}

// NormalizeOCC returns the padded form of an OCC symbol.
func NormalizeOCC(sym string) (string, error) {
	p, err := ParseOCC(sym)
	if err != nil {
		return "", err
	}
	return OCCSymbol(p.Root, p.Expiration, p.Right, p.Strike)
}
