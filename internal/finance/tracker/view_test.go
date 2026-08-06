package tracker

import (
	"testing"
	"time"
)

func TestFreeWorkdays(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	start := day("2026-01-05") // Monday
	today := day("2026-01-12") // Monday (exclusive upper bound)

	holidays := map[string]bool{"2026-01-06": true}                     // Tue is a public holiday
	billable := map[string]bool{"2026-01-07": true, "2026-01-08": true} // Wed, Thu worked

	// Workdays before today: Mon 5, Tue 6 (holiday), Wed 7 (billable), Thu 8
	// (billable), Fri 9. Sat 10 / Sun 11 are weekends. Free = Mon 5 + Fri 9 = 2.
	if got := freeWorkdays(start, today, holidays, billable); got != 2 {
		t.Errorf("freeWorkdays = %d, want 2", got)
	}
}

func TestFreeWorkdaysNoneTakenIsZero(t *testing.T) {
	day := func(s string) time.Time {
		d, _ := time.Parse("2006-01-02", s)
		return d
	}
	start := day("2026-03-02") // Monday
	today := day("2026-03-07") // Saturday

	// Every workday Mon–Fri has billable time → no free days taken.
	billable := map[string]bool{
		"2026-03-02": true, "2026-03-03": true, "2026-03-04": true,
		"2026-03-05": true, "2026-03-06": true,
	}
	if got := freeWorkdays(start, today, map[string]bool{}, billable); got != 0 {
		t.Errorf("freeWorkdays = %d, want 0", got)
	}
}
