package tracker

import (
	"errors"
	"testing"
	"time"
)

func TestFormatNum(t *testing.T) {
	cases := map[float64]string{75: "75", 75.5: "75.5", 75.25: "75.25", 0: "0", 75.20: "75.2"}
	for in, want := range cases {
		if got := formatNum(in); got != want {
			t.Errorf("formatNum(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatHM(t *testing.T) {
	cases := map[float64]string{18.25: "18:15", 9.75: "9:45", 0: "0:00", 1: "1:00", 8.5: "8:30"}
	for in, want := range cases {
		if got := formatHM(in); got != want {
			t.Errorf("formatHM(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatEuro(t *testing.T) {
	cases := map[int]string{136875: "1,369", 0: "0", 50: "1", 49: "0", -136875: "-1,369", 100: "1", 999950: "10,000"}
	for in, want := range cases {
		if got := formatEuro(in); got != want {
			t.Errorf("formatEuro(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupThousands(t *testing.T) {
	cases := map[int]string{0: "0", 12: "12", 123: "123", 1234: "1,234", 1234567: "1,234,567", 12000: "12,000"}
	for in, want := range cases {
		if got := groupThousands(in); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCurrencySymbol(t *testing.T) {
	cases := map[string]string{"USD": "$", "GBP": "£", "JPY": "¥", "EUR": "€", "": "€", "eur": "€", "CHF": "CHF "}
	for in, want := range cases {
		if got := CurrencySymbol(in); got != want {
			t.Errorf("CurrencySymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRound(t *testing.T) {
	if round(1.4) != 1 || round(1.5) != 2 || round(-1.5) != -2 {
		t.Errorf("round behaving unexpectedly")
	}
}

func TestFirstErr(t *testing.T) {
	if firstErr(nil, nil) != "" {
		t.Error("firstErr(nil...) should be empty")
	}
	if got := firstErr(nil, errors.New("second"), errors.New("third")); got != "second" {
		t.Errorf("firstErr = %q, want second", got)
	}
}

func TestAggHours(t *testing.T) {
	// With a rate, hours derive from amount/rate.
	if got := aggHours(Aggregate{RateCents: 7500, AmountCents: 15000}); got != 2 {
		t.Errorf("aggHours with rate = %v, want 2", got)
	}
	// Without a rate, fall back to seconds.
	if got := aggHours(Aggregate{Seconds: 1800}); got != 0.5 {
		t.Errorf("aggHours without rate = %v, want 0.5", got)
	}
}

func TestNavYears(t *testing.T) {
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	got := navYears(now, yearMonth{})
	if len(got) != 5 || got[0] != 2024 || got[2] != 2026 || got[4] != 2028 {
		t.Errorf("navYears = %v, want [2024 2025 2026 2027 2028]", got)
	}
}

func TestWorkdayInfo(t *testing.T) {
	loc := time.UTC
	// March 2026: 1 is Sunday. today = Wed 2026-03-04.
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, loc)
	end := time.Date(2026, 3, 31, 0, 0, 0, 0, loc)
	today := time.Date(2026, 3, 4, 0, 0, 0, 0, loc)
	holidays := map[string]bool{"2026-03-05": true} // Thu is a holiday

	remaining, todayIsWorkday := workdayInfo(start, end, today, holidays)
	if !todayIsWorkday {
		t.Error("Wed 2026-03-04 should be a workday")
	}
	// Workdays strictly after Mar 4, excluding weekends and Mar 5 holiday.
	// Remaining weekdays in March after the 4th: 6,9,10,11,12,13,16..20,23..27,30,31
	// minus the 5th (already excluded since it's not > 4? it is > 4 but holiday).
	// Count: 6 | 9 10 11 12 13 | 16 17 18 19 20 | 23 24 25 26 27 | 30 31 = 1+5+5+5+2 = 18
	if remaining != 18 {
		t.Errorf("remaining workdays = %d, want 18", remaining)
	}
}

func TestWorkdayInfoTodayOnWeekend(t *testing.T) {
	loc := time.UTC
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, loc)
	end := time.Date(2026, 3, 31, 0, 0, 0, 0, loc)
	today := time.Date(2026, 3, 7, 0, 0, 0, 0, loc) // Saturday
	_, todayIsWorkday := workdayInfo(start, end, today, map[string]bool{})
	if todayIsWorkday {
		t.Error("Saturday should not be a workday")
	}
}
