package tracker

import (
	"context"
	"testing"
	"time"
)

// testBudgetJSONStepped is a recurring rent that steps 900 -> 950 in
// January 2027 -> 990 in January 2028, the whole feature in one category.
const testBudgetJSONStepped = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-9000-0000000000a1", "name": "Rent", "amount": 900,
        "amount_changes": [
          { "from": "2027-01-01", "amount": 950 },
          { "from": "2028-01-01", "amount": 990 }
        ]}
    ]}
  ]
}`

func TestAmountChangeStepsFromItsMonth(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONStepped})

	view, err := b.ForMonth(context.Background(), 2026, time.December, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth December 2026: %v", err)
	}
	if got := rowByName(view, "Rent").PlannedCents; got != eurToCents(900) {
		t.Errorf("Rent in December 2026 = %d, want %d (before its first change)", got, eurToCents(900))
	}

	view, err = b.ForMonth(context.Background(), 2027, time.January, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth January 2027: %v", err)
	}
	if got := rowByName(view, "Rent").PlannedCents; got != eurToCents(950) {
		t.Errorf("Rent in January 2027 = %d, want %d (its first change is inclusive)", got, eurToCents(950))
	}

	view, err = b.ForMonth(context.Background(), 2027, time.July, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth July 2027: %v", err)
	}
	if got := rowByName(view, "Rent").PlannedCents; got != eurToCents(950) {
		t.Errorf("Rent in July 2027 = %d, want %d (still under the first change)", got, eurToCents(950))
	}
}

func TestAmountChangeLatestEntryWins(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONStepped})

	view, err := b.ForMonth(context.Background(), 2028, time.January, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth January 2028: %v", err)
	}
	if got := rowByName(view, "Rent").PlannedCents; got != eurToCents(990) {
		t.Errorf("Rent in January 2028 = %d, want %d (the later change supersedes the earlier)", got, eurToCents(990))
	}
}

func TestAmountChangeMinimalModeUsesItsOwnPair(t *testing.T) {
	const withMinimals = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-9000-0000000000b1", "name": "Rent", "amount": 900, "minimal_amount": 800,
        "amount_changes": [
          { "from": "2027-01-01", "amount": 950, "minimal_amount": 850 },
          { "from": "2028-01-01", "amount": 990 }
        ]}
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": withMinimals})

	// 2026: minimal mode still uses the top-level pair.
	view, err := b.ForMonth(context.Background(), 2026, time.July, testNow, true)
	if err != nil {
		t.Fatalf("ForMonth July 2026 minimal: %v", err)
	}
	if got := rowByName(view, "Rent").PlannedCents; got != eurToCents(800) {
		t.Errorf("Rent minimal in 2026 = %d, want %d", got, eurToCents(800))
	}

	// 2027: the change carries its own minimal_amount.
	view, err = b.ForMonth(context.Background(), 2027, time.July, testNow, true)
	if err != nil {
		t.Fatalf("ForMonth July 2027 minimal: %v", err)
	}
	if got := rowByName(view, "Rent").PlannedCents; got != eurToCents(850) {
		t.Errorf("Rent minimal in 2027 = %d, want %d (the change's own minimal_amount)", got, eurToCents(850))
	}

	// 2028: the change has no minimal_amount, so minimal mode falls back to
	// that period's own amount — never the top-level 800, which belongs to a
	// period that is no longer in force.
	view, err = b.ForMonth(context.Background(), 2028, time.July, testNow, true)
	if err != nil {
		t.Fatalf("ForMonth July 2028 minimal: %v", err)
	}
	if got := rowByName(view, "Rent").PlannedCents; got != eurToCents(990) {
		t.Errorf("Rent minimal in 2028 = %d, want %d (the change's own amount)", got, eurToCents(990))
	}
}

func TestAmountChangeOverrideStillWins(t *testing.T) {
	const withOverride = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-9000-0000000000c1", "name": "Rent", "amount": 900,
        "amount_changes": [ { "from": "2027-01-01", "amount": 950 } ],
        "overrides": [ { "month": "2027-02-01", "amount": 0 } ]}
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": withOverride})

	view, err := b.ForMonth(context.Background(), 2027, time.February, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth February 2027: %v", err)
	}
	r := rowByName(view, "Rent")
	if r.PlannedCents != 0 {
		t.Fatalf("Rent in an override month = %d, want 0 — an override wins unconditionally", r.PlannedCents)
	}
	// The zeroed month previews the next one at the changed price.
	if r.UpcomingCents != eurToCents(950) || r.UpcomingMonth != "March 2027" {
		t.Errorf("preview = %d (%s), want %d (March 2027) at the changed price", r.UpcomingCents, r.UpcomingMonth, eurToCents(950))
	}
}

func TestAmountChangeInsideWindowOnly(t *testing.T) {
	const windowed = `{
  "groups": [
    { "name": "Subscriptions", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-9000-0000000000d1", "name": "Agent", "amount": 90,
        "from": "2026-10-01", "until": "2027-06-01",
        "amount_changes": [ { "from": "2027-01-01", "amount": 100 } ]}
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": windowed})

	check := func(m time.Month, year int, want float64) {
		view, err := b.ForMonth(context.Background(), year, m, testNow, false)
		if err != nil {
			t.Fatalf("ForMonth %s %d: %v", m, year, err)
		}
		if got := rowByName(view, "Agent").PlannedCents; got != eurToCents(want) {
			t.Errorf("Agent in %s %d = %d, want %d", m, year, got, eurToCents(want))
		}
	}

	check(time.September, 2026, 0) // before the window
	check(time.December, 2026, 90) // in the window, before the change
	check(time.January, 2027, 100) // in the window, after the change
	check(time.June, 2027, 100)    // in the window's final month
	check(time.July, 2027, 0)      // after the window
}

func TestAmountChangeFeedsTheUpcomingPreview(t *testing.T) {
	// The July 2027 month is zeroed by an override, and the next month is the
	// first one at the changed price — the preview must say 110, not 90.
	const zeroedBeforeChange = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-9000-0000000000e1", "name": "Rent", "amount": 90,
        "amount_changes": [ { "from": "2027-08-01", "amount": 110 } ],
        "overrides": [ { "month": "2027-07-01", "amount": 0 } ]}
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": zeroedBeforeChange})

	view, err := b.ForMonth(context.Background(), 2027, time.July, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth July 2027: %v", err)
	}
	r := rowByName(view, "Rent")
	if r.PlannedCents != 0 {
		t.Fatalf("Rent in the zeroed month = %d, want 0", r.PlannedCents)
	}
	if r.UpcomingCents != eurToCents(110) {
		t.Errorf("upcoming preview = %d, want %d (the price the next month actually costs)", r.UpcomingCents, eurToCents(110))
	}
	if r.UpcomingMonth != "August 2027" {
		t.Errorf("upcoming month = %q, want %q", r.UpcomingMonth, "August 2027")
	}
}

func TestANotYetStartedSteppedCategoryIsHiddenUntilItBegins(t *testing.T) {
	const startsLater = `{
  "groups": [
    { "name": "Subscriptions", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-9000-0000000000f1", "name": "Agent", "amount": 90,
        "from": "2027-05-01",
        "amount_changes": [ { "from": "2027-08-01", "amount": 110 } ]}
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": startsLater})

	// Before it starts there is no announcement at all: no row, no estimate
	// of its from month's price — the window simply has not opened yet.
	view, err := b.ForMonth(context.Background(), 2027, time.April, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth April 2027: %v", err)
	}
	if r := rowByName(view, "Agent"); r.Name != "" {
		t.Errorf("Agent was announced in April 2027, before its from month: %+v", r)
	}

	view, err = b.ForMonth(context.Background(), 2027, time.August, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth August 2027: %v", err)
	}
	if got := rowByName(view, "Agent").PlannedCents; got != eurToCents(110) {
		t.Errorf("Agent in August 2027 = %d, want %d (the changed price)", got, eurToCents(110))
	}
}

func TestAmountChangeYearViewSumsBothPrices(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONStepped})
	// now is February 2027, so the private year sums February through
	// December — all eleven months at 950, since the change landed in
	// January 2027, before the sum starts.
	now := time.Date(2027, time.February, 15, 0, 0, 0, 0, time.UTC)
	view, err := b.ForYear(context.Background(), 2027, now, time.Time{})
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if want := eurToCents(950) * 11; rowByName(view, "Rent").PlannedCents != want {
		t.Errorf("Rent 2027 total = %d, want %d (11 months at the changed price)", rowByName(view, "Rent").PlannedCents, want)
	}

	// A past year reads back at its own price, from any later now.
	view, err = b.ForYear(context.Background(), 2026, now, time.Time{})
	if err != nil {
		t.Fatalf("ForYear 2026: %v", err)
	}
	if want := eurToCents(900) * 12; rowByName(view, "Rent").PlannedCents != want {
		t.Errorf("Rent 2026 total = %d, want %d (still 900 all year)", rowByName(view, "Rent").PlannedCents, want)
	}
}
