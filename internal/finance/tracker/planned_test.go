package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

// plannedBudgetJSON exercises every branch the planned figure can take:
// recurring, a month zeroed by an override, an override with a real price, a
// one-off due in September, and a company-kind group.
const plannedBudgetJSON = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 1000 },
      { "id": "00000000-0000-4000-8000-000000000002", "name": "Gym", "amount": 40,
        "overrides": [{ "month": "2026-08-01", "amount": 0 }] },
      { "id": "00000000-0000-4000-8000-000000000003", "name": "Flight", "amount": 400,
        "minimal_amount": 100,
        "overrides": [{ "month": "2026-09-01", "amount": 427.42 }] }
    ]},
    { "name": "OneOff", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000004", "name": "Desk", "amount": 500, "date": "2026-09-01" }
    ]},
    { "name": "Office", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000005", "name": "Accounting", "amount": 200 }
    ]}
  ]
}`

func plannedBudget(t *testing.T) *Budget {
	t.Helper()
	return newTestBudget(t, map[string]string{"budget.json": plannedBudgetJSON})
}

func byID(cats []PlannedCategory) map[string]PlannedCategory {
	out := map[string]PlannedCategory{}
	for _, c := range cats {
		out[c.ID] = c
	}
	return out
}

func TestPlannedForMonth(t *testing.T) {
	b := plannedBudget(t)

	august, err := b.PlannedForMonth(context.Background(), 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	if len(august) != 5 {
		t.Fatalf("got %d categories, want 5", len(august))
	}
	a := byID(august)

	if got := a["00000000-0000-4000-8000-000000000001"].PlannedCents; got != 100000 {
		t.Errorf("Rent = %d, want 100000", got)
	}
	if got := a["00000000-0000-4000-8000-000000000002"]; got.PlannedCents != 0 || !got.Overridden {
		t.Errorf("Gym = %+v, want 0 cents and Overridden", got)
	}
	if got := a["00000000-0000-4000-8000-000000000003"]; got.PlannedCents != 40000 || got.Overridden {
		t.Errorf("Flight in August = %+v, want its base 40000 and not overridden", got)
	}
	if got := a["00000000-0000-4000-8000-000000000004"].PlannedCents; got != 0 {
		t.Errorf("Desk outside its due month = %d, want 0", got)
	}
	if got := a["00000000-0000-4000-8000-000000000005"]; got.PlannedCents != 20000 || got.Kind != "company" {
		t.Errorf("Accounting = %+v, want 20000 and company kind", got)
	}

	september, err := b.PlannedForMonth(context.Background(), 2026, time.September)
	if err != nil {
		t.Fatal(err)
	}
	s := byID(september)
	if got := s["00000000-0000-4000-8000-000000000004"]; got.PlannedCents != 50000 || got.Date != "2026-09-01" {
		t.Errorf("Desk in its due month = %+v, want 50000 and its date", got)
	}
	if got := s["00000000-0000-4000-8000-000000000003"]; got.PlannedCents != 42742 || !got.Overridden {
		t.Errorf("Flight in September = %+v, want the 427.42 override", got)
	}
	if got := s["00000000-0000-4000-8000-000000000002"].PlannedCents; got != 4000 {
		t.Errorf("Gym in September = %d, want its base 4000 — the override is August only", got)
	}
}

// TestPlannedIgnoresMinimalMode is the reason minimal is hard-wired false:
// the toggle is a transient UI flag, and an API whose figures change because
// someone clicked a button in a browser tab is a bug.
func TestPlannedIgnoresMinimalMode(t *testing.T) {
	b := plannedBudget(t)
	ctx := context.Background()

	before, err := b.PlannedForMonth(ctx, 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	if !b.ToggleMinimal() {
		t.Fatal("ToggleMinimal did not switch minimal mode on")
	}
	after, err := b.PlannedForMonth(ctx, 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}

	bm, am := byID(before), byID(after)
	for id, want := range bm {
		if got := am[id]; got.PlannedCents != want.PlannedCents {
			t.Errorf("%s (%s) = %d with minimal mode on, %d with it off", want.Name, id, got.PlannedCents, want.PlannedCents)
		}
	}

	// Both calls agreeing isn't enough: code that always used minimal mode
	// would agree too. Flight's base is 400 and its minimal 100, so pin the
	// value, not just the stability.
	const flight = "00000000-0000-4000-8000-000000000003"
	if got := am[flight].PlannedCents; got != 40000 {
		t.Errorf("Flight = %d, want its base 40000 — the minimal 10000 means the toggle leaked in", got)
	}

	// And ForMonth *does* move, so the fixture would catch a leak.
	view, err := b.ForMonth(ctx, 2026, time.August, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowByName(view, "Flight").PlannedCents; got != 10000 {
		t.Fatalf("ForMonth Flight = %d, want the minimal 10000 — the fixture no longer proves anything", got)
	}
}

// TestPlannedMatchesForMonth is the assertion that stops the API drifting from
// the dashboard, which is the failure that would quietly poison Hermes'
// matching: every id it offers must carry the figure the page shows.
func TestPlannedMatchesForMonth(t *testing.T) {
	b := plannedBudget(t)
	ctx := context.Background()

	for _, month := range []time.Month{time.January, time.August, time.September, time.December} {
		t.Run(month.String(), func(t *testing.T) {
			view, err := b.ForMonth(ctx, 2026, month, testNow)
			if err != nil {
				t.Fatal(err)
			}
			planned, err := b.PlannedForMonth(ctx, 2026, month)
			if err != nil {
				t.Fatal(err)
			}

			var private, company int
			for _, c := range planned {
				if c.Kind == string(budgetdata.GroupKindCompany) {
					company += c.PlannedCents
					continue
				}
				private += c.PlannedCents
			}
			if private != view.TotalPlannedCents {
				t.Errorf("private total = %d, ForMonth says %d", private, view.TotalPlannedCents)
			}
			if company != view.CompanyTotalPlannedCents {
				t.Errorf("company total = %d, ForMonth says %d", company, view.CompanyTotalPlannedCents)
			}

			// And per category, not just in aggregate: two offsetting errors
			// would pass a totals-only check.
			rows := map[string]int{}
			for _, g := range append(append([]CategoryGroupView{}, view.Groups...), view.CompanyGroups...) {
				for _, r := range g.Rows {
					rows[r.CategoryID] = r.PlannedCents
				}
			}
			for _, c := range planned {
				if want, ok := rows[c.ID]; ok && want != c.PlannedCents {
					t.Errorf("%s: PlannedForMonth says %d, the dashboard row says %d", c.Name, c.PlannedCents, want)
				}
			}
		})
	}
}

func TestPlannedByMonth(t *testing.T) {
	b := plannedBudget(t)
	all, err := b.PlannedByMonth(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 12 {
		t.Fatalf("got %d months, want 12", len(all))
	}
	if got := byID(all["2026-09"])["00000000-0000-4000-8000-000000000004"].PlannedCents; got != 50000 {
		t.Errorf("Desk in 2026-09 = %d, want 50000", got)
	}
	if got := byID(all["2026-08"])["00000000-0000-4000-8000-000000000004"].PlannedCents; got != 0 {
		t.Errorf("Desk in 2026-08 = %d, want 0", got)
	}
	// Every month must carry every category, so a caller can trust the shape.
	for key, cats := range all {
		if len(cats) != 5 {
			t.Errorf("%s has %d categories, want 5", key, len(cats))
		}
	}
}

// TestPlannedByMonthHasNoHiddenNowDependence: the same year must return the
// same figures whenever it is asked, unlike ForYear, whose private range
// starts at today's month.
func TestPlannedByMonthHasNoHiddenNowDependence(t *testing.T) {
	b := plannedBudget(t)
	ctx := context.Background()

	first, err := b.PlannedByMonth(ctx, 2026)
	if err != nil {
		t.Fatal(err)
	}
	sum := func(m map[string][]PlannedCategory) int {
		total := 0
		for _, cats := range m {
			for _, c := range cats {
				total += c.PlannedCents
			}
		}
		return total
	}
	// ForYear would differ between these two; PlannedByMonth cannot, because
	// it never consults the clock.
	second, err := b.PlannedByMonth(ctx, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if sum(first) != sum(second) {
		t.Errorf("two calls disagreed: %d vs %d", sum(first), sum(second))
	}
}

func TestCategoryIndex(t *testing.T) {
	b := plannedBudget(t)
	idx, err := b.CategoryIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 5 {
		t.Fatalf("got %d categories, want 5", len(idx))
	}
	desk := idx["00000000-0000-4000-8000-000000000004"]
	if desk.Name != "Desk" || desk.Group != "OneOff" || desk.Kind != "private" || desk.Date != "2026-09-01" {
		t.Errorf("Desk = %+v", desk)
	}
	if got := idx["00000000-0000-4000-8000-000000000005"].Kind; got != "company" {
		t.Errorf("Accounting kind = %q, want company", got)
	}
}

func TestPlannedPropagatesABrokenBudget(t *testing.T) {
	b := newTestBudget(t, nil) // no budget.json at all
	ctx := context.Background()
	if _, err := b.PlannedForMonth(ctx, 2026, time.August); err == nil {
		t.Error("PlannedForMonth: want an error when budget.json is missing")
	}
	if _, err := b.PlannedByMonth(ctx, 2026); err == nil {
		t.Error("PlannedByMonth: want an error")
	}
	if _, err := b.CategoryIndex(ctx); err == nil {
		t.Error("CategoryIndex: want an error")
	}
}
