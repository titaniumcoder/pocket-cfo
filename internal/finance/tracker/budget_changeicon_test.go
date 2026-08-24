package tracker

import (
	"context"
	"testing"
	"time"
)

// TestCategoryRowPointsAtTheNextAmountChange: the page points at the month
// the price moves instead of making the reader diff budget.json. The arrow
// shows for a change strictly after the viewed month, within the same
// horizon the upcoming-estimate previews use.
func TestCategoryRowPointsAtTheNextAmountChange(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONStepped})

	// December 2026: the row pays 900 and the arrow points at January 2027.
	view, err := b.ForMonth(context.Background(), 2026, time.December, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth December 2026: %v", err)
	}
	r := rowByName(view, "Rent")
	if r.ScheduledChangeURL != "/2027/1" {
		t.Errorf("arrow URL = %q, want /2027/1", r.ScheduledChangeURL)
	}
	if r.ScheduledChangeTooltip != "950 from January 2027" {
		t.Errorf("arrow tooltip = %q, want the new price and its month", r.ScheduledChangeTooltip)
	}

	// January 2027: that change is the price the row already shows, so the
	// arrow moves on to the next one, in 2028.
	view, err = b.ForMonth(context.Background(), 2027, time.January, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth January 2027: %v", err)
	}
	r = rowByName(view, "Rent")
	if r.ScheduledChangeURL != "/2028/1" {
		t.Errorf("arrow URL = %q, want /2028/1 — a change in the viewed month is already the price shown", r.ScheduledChangeURL)
	}
	if r.ScheduledChangeTooltip != "990 from January 2028" {
		t.Errorf("arrow tooltip = %q, want the 2028 step", r.ScheduledChangeTooltip)
	}

	// Once every scheduled change is in the past, the row goes quiet.
	view, err = b.ForMonth(context.Background(), 2030, time.January, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth January 2030: %v", err)
	}
	if r := rowByName(view, "Rent"); r.ScheduledChangeURL != "" {
		t.Errorf("a category with no upcoming change still carries an arrow: %+v", r.ScheduledChangeURL)
	}
}

func TestCategoryRowArrowRespectsTheLookahead(t *testing.T) {
	const farAway = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-9000-000000000101", "name": "Rent", "amount": 900,
        "amount_changes": [ { "from": "2030-07-01", "amount": 1200 } ]}
    ]}
  ]
}`
	b := newTestBudget(t, map[string]string{"budget.json": farAway})

	// 24 months out is the last one that counts.
	view, err := b.ForMonth(context.Background(), 2028, time.July, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth July 2028: %v", err)
	}
	if r := rowByName(view, "Rent"); r.ScheduledChangeURL != "/2030/7" {
		t.Errorf("arrow URL = %q, want /2030/7 (the horizon's last month)", r.ScheduledChangeURL)
	}

	// One month earlier, the same change is 25 months out and not news yet.
	view, err = b.ForMonth(context.Background(), 2028, time.June, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth June 2028: %v", err)
	}
	if r := rowByName(view, "Rent"); r.ScheduledChangeURL != "" {
		t.Errorf("a change beyond the preview horizon carries an arrow: %q", r.ScheduledChangeURL)
	}
}

func TestCategoryRowFlatAmountHasNoArrow(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	view, err := b.ForMonth(context.Background(), 2026, time.January, testNow, false)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if r := rowByName(view, "Rent"); r.ScheduledChangeURL != "" {
		t.Errorf("a flat category carries an arrow: %q", r.ScheduledChangeURL)
	}
}
