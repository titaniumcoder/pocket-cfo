package tracker

import (
	"testing"
	"time"
)

var startNow = time.Date(2026, time.August, 13, 0, 0, 0, 0, time.UTC)

// TestTheRouterAndThePickerAgree is the whole reason NavBounds exists. The
// router used to carry its own yearRange constant that happened to match the
// tracker's; two copies of a rule is how a URL starts answering for a month the
// picker refuses to offer.
func TestTheRouterAndThePickerAgree(t *testing.T) {
	start := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)

	f := &Figures{}
	f.fillMonthNav(startNow, time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC), floorOf(start))

	offered := map[int]bool{}
	for _, m := range f.Months {
		offered[m.Num] = true
	}
	for month := 1; month <= 12; month++ {
		inPicker := offered[month]
		inRouter := MonthIsOffered(2026, month, startNow, start)
		if inPicker != inRouter {
			t.Errorf("month %d: picker offers %v, router accepts %v", month, inPicker, inRouter)
		}
	}
	// And what it agrees on is the start month.
	for month := 1; month <= 3; month++ {
		if MonthIsOffered(2026, month, startNow, start) {
			t.Errorf("month %d is before budgeting began and is still offered", month)
		}
	}
	if !MonthIsOffered(2026, 4, startNow, start) {
		t.Error("the start month itself is not offered")
	}
}

// TestArrowsStopAtTheStartMonth: the arrows used to compare years, which is why
// stepping back from January always worked. A floor is a month, not a year.
func TestArrowsStopAtTheStartMonth(t *testing.T) {
	floor := yearMonth{2026, time.April}

	at := func(m time.Month) MonthNav {
		return monthNav(startNow, time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC), floor, monthURL)
	}
	if nav := at(time.April); !nav.PrevDisabled {
		t.Errorf("April is the first budgeted month and Previous is live (%s)", nav.PrevURL)
	}
	if nav := at(time.May); nav.PrevDisabled {
		t.Error("May cannot step back to April")
	}
	// Without a floor nothing changes: the ±2-year window still governs.
	if nav := monthNav(startNow, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), yearMonth{}, monthURL); nav.PrevDisabled {
		t.Error("with no start month, January can no longer step back into the previous year")
	}
	if got := len(at(time.April).Months); got != 9 {
		t.Errorf("the picker offers %d months of the start year, want 9 (April–December)", got)
	}
	// A later year is whole again.
	later := monthNav(startNow, time.Date(2027, time.February, 1, 0, 0, 0, 0, time.UTC), floor, monthURL)
	if len(later.Months) != 12 {
		t.Errorf("2027 offers %d months, want all 12", len(later.Months))
	}
}

// TestNavBoundsNeverWidensTheWindow: a start month is a floor, not a licence to
// browse further back than the ±2 years always allowed.
func TestNavBoundsNeverWidensTheWindow(t *testing.T) {
	ancient := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	minYear, maxYear := NavBounds(startNow, ancient)
	if minYear != 2024 || maxYear != 2028 {
		t.Errorf("bounds = %d..%d, want 2024..2028 — an old start month must not widen the window", minYear, maxYear)
	}
	// A start inside the window raises the floor.
	if minYear, _ := NavBounds(startNow, time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)); minYear != 2026 {
		t.Errorf("minYear = %d, want 2026", minYear)
	}
	// And no start at all leaves the old behaviour untouched.
	if minYear, maxYear := NavBounds(startNow, time.Time{}); minYear != 2024 || maxYear != 2028 {
		t.Errorf("unbounded = %d..%d, want 2024..2028", minYear, maxYear)
	}
}

// TestTheStartYearIsMeasuredAgainstItsOwnMonths: a year that began in April has
// nine months, and judging it against twelve means it can never read as
// reconciled however complete it is.
func TestTheStartYearIsMeasuredAgainstItsOwnMonths(t *testing.T) {
	files := map[string]string{}
	for m := time.April; m <= time.December; m++ {
		files[actualsPath(2026, m)] = `{"month":"` + monthKey(2026, m) + `","coverage":[{"account":"A","from":"` +
			monthKey(2026, m) + `-01","to":"` + monthKey(2026, m) + `-28","imported_at":"2026-12-31"}],
			"transactions":[{"id":"x` + monthKey(2026, m) + `","date":"` + monthKey(2026, m) +
			`-05","description":"L","amount":10,"account":"A","category":"rent"}]}`
	}
	a := newTestActuals(t, files)
	start := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)

	// Coverage does not reach month end here, so Complete is false either way;
	// what matters is that the nine present months are not judged against 12.
	bounded, err := a.ForYear(t.Context(), 2026, start)
	if err != nil {
		t.Fatal(err)
	}
	unbounded, err := a.ForYear(t.Context(), 2026, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if bounded.TotalCents != unbounded.TotalCents {
		t.Errorf("bounded total %d, unbounded %d — the same nine months are present either way",
			bounded.TotalCents, unbounded.TotalCents)
	}

	// The months before the start are not even read.
	if got, err := a.UntrackedMonths(t.Context(), 2026, start); err != nil || len(got) != 0 {
		t.Errorf("UntrackedMonths = %v, %v", got, err)
	}
}

// TestYearAggregationStartsAtTheStartMonth: three months of nothing counted
// into the start year would report a year that underspent by a quarter.
func TestYearAggregationStartsAtTheStartMonth(t *testing.T) {
	past := time.Date(2027, time.June, 1, 0, 0, 0, 0, time.UTC) // so 2026 is a past year
	if got := privateExpenseStartMonth(2026, past, yearMonth{2026, time.April}); got != time.April {
		t.Errorf("start month = %v, want April", got)
	}
	if got := privateExpenseStartMonth(2027, past, yearMonth{2026, time.April}); got != time.June {
		t.Errorf("the current year still starts at now (%v)", got)
	}
	if got := privateExpenseStartMonth(2028, past, yearMonth{2026, time.April}); got != time.January {
		t.Errorf("a later year starts at %v, want January", got)
	}
	// The funding range follows for free, which is the invariant funding.go
	// says must never drift.
	fs, _ := fundingRangeForYear(2026, past, yearMonth{2026, time.April})
	if want := (yearMonth{2026, time.February}); fs != want {
		t.Errorf("funding starts %v, want %v — two months before the expense range", fs, want)
	}
}
