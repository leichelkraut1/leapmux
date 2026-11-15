package tradinglab

import (
	"time"
	_ "time/tzdata" // New York time on a computer without a zone database
)

// newYork is the US market's time zone.
var newYork = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// Session is one regular US equity and options trading session.
type Session struct {
	Open  time.Time `json:"open"`
	Close time.Time `json:"close"`
}

// MarketSession reports whether the regular session is open at t, and the
// session that is open then or, when the market is closed, the latest one
// before t. It knows the NYSE holidays and early closes; it does not know
// an unscheduled closure.
func MarketSession(t time.Time) (open bool, s Session) {
	day := t.In(newYork)
	for range 15 {
		if s, ok := sessionOn(day); ok && !t.Before(s.Open) {
			return t.Before(s.Close), s
		}
		day = day.AddDate(0, 0, -1)
	}
	return false, Session{}
}

// sessionOn is the regular session on the New York date of day, if any.
func sessionOn(day time.Time) (Session, bool) {
	y, m, d := day.Date()
	date := time.Date(y, m, d, 0, 0, 0, 0, newYork)
	if wd := date.Weekday(); wd == time.Saturday || wd == time.Sunday || marketHoliday(date) {
		return Session{}, false
	}
	closeHour := 16
	if earlyClose(date) {
		closeHour = 13
	}
	return Session{
		Open:  time.Date(y, m, d, 9, 30, 0, 0, newYork),
		Close: time.Date(y, m, d, closeHour, 0, 0, 0, newYork),
	}, true
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// observed moves a holiday that falls on a weekend to the nearest weekday.
// NYSE does not move New Year's Day back into the old year, which
// marketHoliday handles.
func observed(t time.Time) time.Time {
	switch t.Weekday() {
	case time.Saturday:
		return t.AddDate(0, 0, -1)
	case time.Sunday:
		return t.AddDate(0, 0, 1)
	}
	return t
}

// nthWeekday is the nth weekday of a month; n < 0 counts from the end.
func nthWeekday(y int, m time.Month, wd time.Weekday, n int) time.Time {
	if n > 0 {
		t := time.Date(y, m, 1, 0, 0, 0, 0, newYork)
		for t.Weekday() != wd {
			t = t.AddDate(0, 0, 1)
		}
		return t.AddDate(0, 0, 7*(n-1))
	}
	t := time.Date(y, m+1, 0, 0, 0, 0, 0, newYork)
	for t.Weekday() != wd {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

// easter is Easter Sunday (the anonymous Gregorian algorithm).
func easter(y int) time.Time {
	a, b, c := y%19, y/100, y%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return time.Date(y, time.Month(month), day, 0, 0, 0, 0, newYork)
}

func marketHoliday(date time.Time) bool {
	y := date.Year()
	newYear := time.Date(y, time.January, 1, 0, 0, 0, 0, newYork)
	if newYear.Weekday() != time.Saturday && sameDay(date, observed(newYear)) {
		return true
	}
	for _, h := range []time.Time{
		nthWeekday(y, time.January, time.Monday, 3),  // Martin Luther King Jr. Day
		nthWeekday(y, time.February, time.Monday, 3), // Washington's Birthday
		easter(y).AddDate(0, 0, -2),                  // Good Friday
		nthWeekday(y, time.May, time.Monday, -1),     // Memorial Day
		observed(time.Date(y, time.June, 19, 0, 0, 0, 0, newYork)),
		observed(time.Date(y, time.July, 4, 0, 0, 0, 0, newYork)),
		nthWeekday(y, time.September, time.Monday, 1),  // Labor Day
		nthWeekday(y, time.November, time.Thursday, 4), // Thanksgiving
		observed(time.Date(y, time.December, 25, 0, 0, 0, 0, newYork)),
	} {
		if sameDay(date, h) {
			return true
		}
	}
	return false
}

// earlyClose is a 1 p.m. close: the day before Independence Day, the day
// after Thanksgiving and Christmas Eve, when each is a trading day.
func earlyClose(date time.Time) bool {
	y := date.Year()
	return sameDay(date, time.Date(y, time.July, 3, 0, 0, 0, 0, newYork)) ||
		sameDay(date, nthWeekday(y, time.November, time.Thursday, 4).AddDate(0, 0, 1)) ||
		sameDay(date, time.Date(y, time.December, 24, 0, 0, 0, 0, newYork))
}
