package tracker

import (
	"context"
	"testing"
	"time"
)

// testBudgetJSONWindowed carries two bounded recurring costs: "Ended" ran
// until August 2026, and "Starts" begins in October 2026. Both are recur in
// every other month but must only count inside their window.
const testBudgetJSONWindowed = `{
  "groups": [
    { "name": "Subscriptions", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000011", "name": "Ended", "amount": 180, "until": "2026-08-01" },
      { "id": "00000000-0000-4000-8000-000000000012", "name": "Starts", "amount": 90, "from": "2026-10-01" }
    ]}
  ]
}`

// TestWindowedCategoryCountsInsideUntil verifies an ended recurring cost's
// final month is inclusive: August still counts, September (and every month
// after) does not.
func TestWindowedCategoryCountsInsideUntil(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWindowed})

	inside, err := b.ForMonth(context.Background(), 2026, time.August, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth August: %v", err)
	}
	if want := eurToCents(180); rowByName(inside, "Ended").PlannedCents != want {
		t.Errorf("Ended in August = %d, want %d", rowByName(inside, "Ended").PlannedCents, want)
	}

	// Outside its window means strictly after August; the months before it are
	// inside, so only September onward drops to 0.
	for _, m := range []time.Month{time.September, time.October, time.November, time.December} {
		view, err := b.ForMonth(context.Background(), 2026, m, testNow, false)
		if err != nil {
			t.Fatalf("ForMonth %s: %v", m, err)
		}
		if got := rowByName(view, "Ended").PlannedCents; got != 0 {
			t.Errorf("Ended in %s = %d, want 0 (after its until month)", m, got)
		}
	}

	// And it still counts in every month up to and including August.
	for _, m := range []time.Month{time.January, time.February, time.July, time.August} {
		view, err := b.ForMonth(context.Background(), 2026, m, testNow, false)
		if err != nil {
			t.Fatalf("ForMonth %s: %v", m, err)
		}
		if want := eurToCents(180); rowByName(view, "Ended").PlannedCents != want {
			t.Errorf("Ended in %s = %d, want %d", m, rowByName(view, "Ended").PlannedCents, want)
		}
	}
}

// TestWindowedCategoryCountsFromStartOnward verifies a not-yet-started
// recurring cost contributes nothing before its from month and everything
// from it on.
func TestWindowedCategoryCountsFromStartOnward(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWindowed})

	for _, m := range []time.Month{time.January, time.September} {
		view, err := b.ForMonth(context.Background(), 2026, m, testNow, false)
		if err != nil {
			t.Fatalf("ForMonth %s: %v", m, err)
		}
		if got := rowByName(view, "Starts").PlannedCents; got != 0 {
			t.Errorf("Starts in %s = %d, want 0 (before its from month)", m, got)
		}
	}
	for _, m := range []time.Month{time.October, time.November, time.December} {
		view, err := b.ForMonth(context.Background(), 2026, m, testNow, false)
		if err != nil {
			t.Fatalf("ForMonth %s: %v", m, err)
		}
		if want := eurToCents(90); rowByName(view, "Starts").PlannedCents != want {
			t.Errorf("Starts in %s = %d, want %d", m, rowByName(view, "Starts").PlannedCents, want)
		}
	}
}

// TestWindowedCategoryForYearSumsOnlyInWindowMonths: the private year view
// counts only the months from "now" (July) through December, so a category
// that ends in August contributes July+August, and one that starts in
// October contributes Oct+Nov+Dec — never all twelve, never one.
func TestWindowedCategoryForYearSumsOnlyInWindowMonths(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWindowed})
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	start := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

	view, err := b.ForYear(context.Background(), 2026, now, start)
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if want := eurToCents(180) * 2; rowByName(view, "Ended").PlannedCents != want {
		t.Errorf("Ended 2026 total = %d, want %d (Jul-Aug)", rowByName(view, "Ended").PlannedCents, want)
	}
	if want := eurToCents(90) * 3; rowByName(view, "Starts").PlannedCents != want {
		t.Errorf("Starts 2026 total = %d, want %d (Oct-Dec)", rowByName(view, "Starts").PlannedCents, want)
	}
}

// TestWindowedCategoryCompanyExpensesByMonth confirms the roll-forward's
// month-by-month company expense feed honours the window too, so an ended
// company cost stops draining the cascade after its until month.
func TestWindowedCategoryCompanyExpensesByMonth(t *testing.T) {
	const companyWindowed = `{
  "groups": [
    { "name": "Office", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000021", "name": "Agent", "amount": 200, "until": "2026-08-01" }
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": companyWindowed})
	byMonth, err := b.CompanyExpensesByMonth(context.Background(), 2026, time.Time{})
	if err != nil {
		t.Fatalf("CompanyExpensesByMonth: %v", err)
	}
	for m := time.January; m <= time.December; m++ {
		want := 0
		if m <= time.August {
			want = eurToCents(200)
		}
		if byMonth[m] != want {
			t.Errorf("company expense in %s = %d, want %d", m, byMonth[m], want)
		}
	}
}

// TestWindowedCategoryWithZeroedMonthInsideWindow: a zero override inside the
// window zeroes that month, but the category still counts its neighbours.
func TestWindowedCategoryWithZeroedMonthInsideWindow(t *testing.T) {
	const withOverride = `{
  "groups": [
    { "name": "Subscriptions", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000031", "name": "Ended", "amount": 180, "until": "2026-08-01",
        "overrides": [ { "month": "2026-07-01", "amount": 0 } ] }
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": withOverride})

	jul, err := b.ForMonth(context.Background(), 2026, time.July, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth July: %v", err)
	}
	if got := rowByName(jul, "Ended").PlannedCents; got != 0 {
		t.Errorf("Ended in July (overridden to 0) = %d, want 0", got)
	}

	aug, err := b.ForMonth(context.Background(), 2026, time.August, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth August: %v", err)
	}
	if want := eurToCents(180); rowByName(aug, "Ended").PlannedCents != want {
		t.Errorf("Ended in August = %d, want %d", rowByName(aug, "Ended").PlannedCents, want)
	}
}

// TestEndedCategoryDisappearsFromMonthView: after a recurring cost's window
// is over it must not surface as a visible 0,00 row — the row is gone, not
// blank. rowByName returns a zero row for an absent category, so its Name is
// empty exactly when the row was dropped.
func TestEndedCategoryDisappearsFromMonthView(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWindowed})

	inside, err := b.ForMonth(context.Background(), 2026, time.August, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth August: %v", err)
	}
	if r := rowByName(inside, "Ended"); r.Name == "" {
		t.Error("Ended was hidden in its final month August")
	}

	after, err := b.ForMonth(context.Background(), 2026, time.September, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth September: %v", err)
	}
	if r := rowByName(after, "Ended"); r.Name != "" {
		t.Errorf("Ended still shows in September as a row: %+v", r)
	}
}

// TestNotYetStartedCategoryShowsUpcomingEstimate: before its from month a
// recurring cost appears as the grey future estimate rather than a 0 row.
func TestNotYetStartedCategoryShowsUpcomingEstimate(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWindowed})

	before, err := b.ForMonth(context.Background(), 2026, time.September, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth September: %v", err)
	}
	r := rowByName(before, "Starts")
	if r.Name == "" {
		t.Fatal("Starts was hidden in the month before its from month")
	}
	if r.PlannedCents != 0 {
		t.Errorf("Starts planned in September = %d, want 0", r.PlannedCents)
	}
	if want := eurToCents(90); r.UpcomingCents != want {
		t.Errorf("Starts upcoming = %d, want %d", r.UpcomingCents, want)
	}
	if r.UpcomingMonth != "October 2026" {
		t.Errorf("Starts upcoming month = %q, want October 2026", r.UpcomingMonth)
	}

	first, err := b.ForMonth(context.Background(), 2026, time.October, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth October: %v", err)
	}
	started := rowByName(first, "Starts")
	if started.PlannedCents != eurToCents(90) {
		t.Errorf("Starts planned in October = %d, want 90", started.PlannedCents)
	}
	if started.UpcomingMonth != "" {
		t.Errorf("Starts still carried an upcoming month once active: %q", started.UpcomingMonth)
	}
}

// TestEndedCategoryNeverReturnsAsUpcoming: nextNonZeroMonth must not look
// past the until month, so an ended subscription does not resurface as a
// zeroed-recurring preview in month 25.
func TestEndedCategoryNeverReturnsAsUpcoming(t *testing.T) {
	const endedWithZeroOverride = `{
  "groups": [
    { "name": "Subscriptions", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000041", "name": "Ended", "amount": 180, "until": "2026-08-01",
        "overrides": [ { "month": "2026-07-01", "amount": 0 } ] }
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": endedWithZeroOverride})

	view, err := b.ForMonth(context.Background(), 2028, time.March, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if r := rowByName(view, "Ended"); r.Name != "" {
		t.Errorf("two years after ending, Ended resurfaced as a preview row: %+v", r)
	}
}
