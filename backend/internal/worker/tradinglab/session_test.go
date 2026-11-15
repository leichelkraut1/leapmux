package tradinglab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func ny(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, newYork)
}

func TestMarketSessionKnowsWeekendsHolidaysAndEarlyCloses(t *testing.T) {
	friday := Session{Open: ny(2026, time.November, 13, 9, 30), Close: ny(2026, time.November, 13, 16, 0)}
	cases := []struct {
		name string
		at   time.Time
		open bool
		last Session
	}{
		{"a weekday morning", ny(2026, time.November, 18, 10, 0), true, Session{Open: ny(2026, time.November, 18, 9, 30), Close: ny(2026, time.November, 18, 16, 0)}},
		{"a Sunday", ny(2026, time.November, 15, 14, 0), false, friday},
		{"Friday after the close", ny(2026, time.November, 13, 17, 0), false, friday},
		{"Monday before the open", ny(2026, time.November, 16, 8, 0), false, friday},
		{"Thanksgiving", ny(2026, time.November, 26, 12, 0), false, Session{Open: ny(2026, time.November, 25, 9, 30), Close: ny(2026, time.November, 25, 16, 0)}},
		{"the day after Thanksgiving closes at 1", ny(2026, time.November, 27, 14, 0), false, Session{Open: ny(2026, time.November, 27, 9, 30), Close: ny(2026, time.November, 27, 13, 0)}},
		{"Good Friday", ny(2026, time.April, 3, 12, 0), false, Session{Open: ny(2026, time.April, 2, 9, 30), Close: ny(2026, time.April, 2, 16, 0)}},
		{"Independence Day observed on Friday", ny(2026, time.July, 3, 12, 0), false, Session{Open: ny(2026, time.July, 2, 9, 30), Close: ny(2026, time.July, 2, 16, 0)}},
		{"Christmas Eve closes at 1", ny(2026, time.December, 24, 12, 0), true, Session{Open: ny(2026, time.December, 24, 9, 30), Close: ny(2026, time.December, 24, 13, 0)}},
		{"New Year's Day on a Saturday leaves Dec 31 open", ny(2027, time.December, 31, 12, 0), true, Session{Open: ny(2027, time.December, 31, 9, 30), Close: ny(2027, time.December, 31, 16, 0)}},
		{"New Year's Day on a Sunday closes Monday", ny(2023, time.January, 2, 12, 0), false, Session{Open: ny(2022, time.December, 30, 9, 30), Close: ny(2022, time.December, 30, 16, 0)}},
	}
	for _, c := range cases {
		open, s := MarketSession(c.at.UTC())
		assert.Equal(t, c.open, open, c.name)
		assert.True(t, c.last.Open.Equal(s.Open) && c.last.Close.Equal(s.Close), "%s: got %v", c.name, s)
	}
}
