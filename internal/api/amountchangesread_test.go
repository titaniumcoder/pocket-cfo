package api

import (
	"context"
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// steppedBudget carries one recurring rent that steps 900 -> 930 in January
// 2027 (with its own minimal) -> 960 in January 2028, and one flat grocery
// category with no changes at all, so the surface can be pinned against both
// shapes.
const steppedBudget = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "` + idRent + `", "name": "Rent", "amount": 900,
        "amount_changes": [
          { "from": "2027-01-01", "amount": 930, "minimal_amount": 850 },
          { "from": "2028-01-01", "amount": 960 }
        ]},
      { "id": "` + idGroceries + `", "name": "Groceries", "amount": 350 }
    ]}
  ]
}`

func steppedService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		Budget: &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(steppedBudget)}}},
	}
}

func changeByFrom(changes []budgetdata.AmountChange, from string) (budgetdata.AmountChange, bool) {
	for _, c := range changes {
		if c.From == from {
			return c, true
		}
	}
	return budgetdata.AmountChange{}, false
}

// TestListBudgetCategoriesCarriesTheWholeChangeList: an agent scheduling a new
// change must see every month already spoken for, so the full list — not just
// the next one — is what the surface reports.
func TestListBudgetCategoriesCarriesTheWholeChangeList(t *testing.T) {
	s := steppedService(t)

	cats, err := s.Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rent := categoryByID(t, cats, idRent)

	if len(rent.AmountChanges) != 2 {
		t.Fatalf("rent carries %d changes, want both: %+v", len(rent.AmountChanges), rent.AmountChanges)
	}
	first, ok := changeByFrom(rent.AmountChanges, "2027-01-01")
	if !ok || first.Amount != 930 {
		t.Errorf("first change = %+v, want 2027-01-01 at 930", first)
	}
	if first.MinimalAmount == nil || *first.MinimalAmount != 850 {
		t.Errorf("first change's minimal = %v, want 850", first.MinimalAmount)
	}
	second, ok := changeByFrom(rent.AmountChanges, "2028-01-01")
	if !ok || second.Amount != 960 {
		t.Errorf("second change = %+v, want 2028-01-01 at 960", second)
	}
	if second.MinimalAmount != nil {
		t.Errorf("second change has minimal %v, want none", *second.MinimalAmount)
	}
	// A flat category carries no changes at all, not an empty-but-present list.
	groceries := categoryByID(t, cats, idGroceries)
	if len(groceries.AmountChanges) != 0 {
		t.Errorf("a flat category reports %d changes, want none", len(groceries.AmountChanges))
	}
}

// TestGetBudgetCarriesTheChangeList: the month buckets report the same full
// list, so get_budget and list_budget_categories cannot drift apart.
func TestGetBudgetCarriesTheChangeList(t *testing.T) {
	s := steppedService(t)

	mb, err := s.BudgetForMonth(context.Background(), "2027-06")
	if err != nil {
		t.Fatal(err)
	}
	rent := plannedCategoryByID(t, mb.Categories, idRent)
	if len(rent.AmountChanges) != 2 {
		t.Fatalf("get_budget's rent carries %d changes, want both", len(rent.AmountChanges))
	}
	second, ok := changeByFrom(rent.AmountChanges, "2028-01-01")
	if !ok || second.Amount != 960 {
		t.Errorf("get_budget's 2028 change = %+v, want 960", second)
	}
}

// TestCategoryOmitsAnEmptyChangeListFromItsJSON: the field is omitempty, so a
// budget with no step changes anywhere serialises exactly as it always did —
// an old client reading the payload sees no new key at all.
func TestCategoryOmitsAnEmptyChangeListFromItsJSON(t *testing.T) {
	s := steppedService(t)

	cats, err := s.Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	groceries := categoryByID(t, cats, idGroceries)
	raw, err := json.Marshal(groceries)
	if err != nil {
		t.Fatal(err)
	}
	if containsKey(raw, "amount_changes") {
		t.Errorf("a flat category's JSON still spells the key: %s", raw)
	}

	rent := categoryByID(t, cats, idRent)
	raw, err = json.Marshal(rent)
	if err != nil {
		t.Fatal(err)
	}
	if !containsKey(raw, "amount_changes") {
		t.Errorf("a stepped category's JSON dropped the key: %s", raw)
	}
}

func categoryByID(t *testing.T, cats []Category, id string) Category {
	t.Helper()
	for _, c := range cats {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no category %q in the list", id)
	return Category{}
}

func plannedCategoryByID(t *testing.T, cats []PlannedCategory, id string) PlannedCategory {
	t.Helper()
	for _, c := range cats {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no category %q in the plan", id)
	return PlannedCategory{}
}

func containsKey(raw []byte, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}
