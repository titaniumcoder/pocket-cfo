package budgetdata

import (
	"strings"
	"testing"
)

func changeCategory(id string, changes []AmountChange) Category {
	c := Category{Id: id, Name: id, Amount: 180}
	c.AmountChanges = changes
	return c
}

func changeFile(id string, changes []AmountChange) BudgetFile {
	return BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{changeCategory(id, changes)}}}}
}

func f64Or(v float64) *float64 { return &v }

func TestAmountChangesPlainValid(t *testing.T) {
	f := changeFile("rent", []AmountChange{{From: "2027-01-01", Amount: 950}})
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAmountChangesWithMinimalValid(t *testing.T) {
	f := changeFile("rent", []AmountChange{{From: "2027-01-01", Amount: 950, MinimalAmount: f64Or(800)}})
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAmountChangesWithOneOffDateRefused(t *testing.T) {
	date := "2026-09-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{{
		Id: "oneoff", Name: "oneoff", Amount: 180, Date: &date,
		AmountChanges: []AmountChange{{From: "2027-01-01", Amount: 200}},
	}}}}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("a one-off with amount_changes was accepted")
	} else if !strings.Contains(err.Error(), "single price") {
		t.Errorf("error = %q, want it to say a one-off is a single price", err)
	}
}

func TestAmountChangesDuplicateMonthRefused(t *testing.T) {
	f := changeFile("rent", []AmountChange{{From: "2027-01-01", Amount: 950}, {From: "2027-01-15", Amount: 960}})
	err := ValidateBudget(f)
	if err == nil {
		t.Fatal("two changes in one month were accepted")
	}
	if !strings.Contains(err.Error(), "coin toss") {
		t.Errorf("error = %q, want it to say which amount is in force is a coin toss", err)
	}
}

func TestAmountChangesMinimalAboveOwnAmountRefused(t *testing.T) {
	f := changeFile("rent", []AmountChange{{From: "2027-01-01", Amount: 950, MinimalAmount: f64Or(1000)}})
	err := ValidateBudget(f)
	if err == nil {
		t.Fatal("a change whose minimal_amount exceeds its own amount was accepted")
	}
	if !strings.Contains(err.Error(), "minimal_amount greater than its own amount") {
		t.Errorf("error = %q, want it to name the minimal_amount problem", err)
	}
}

func TestAmountChangesBeforeOwnFromRefused(t *testing.T) {
	from := "2026-10-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{{
		Id: "starts", Name: "starts", Amount: 90, From: &from,
		AmountChanges: []AmountChange{{From: "2026-06-01", Amount: 100}},
	}}}}}
	err := ValidateBudget(f)
	if err == nil {
		t.Fatal("a change before the category's own from was accepted")
	}
	if !strings.Contains(err.Error(), "could never take effect") {
		t.Errorf("error = %q, want it to say the change could never take effect", err)
	}
}

func TestAmountChangesAfterOwnUntilRefused(t *testing.T) {
	until := "2026-08-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{{
		Id: "ends", Name: "ends", Amount: 180, Until: &until,
		AmountChanges: []AmountChange{{From: "2026-09-01", Amount: 200}},
	}}}}}
	err := ValidateBudget(f)
	if err == nil {
		t.Fatal("a change after the category's own until was accepted")
	}
	if !strings.Contains(err.Error(), "could never take effect") {
		t.Errorf("error = %q, want it to say the change could never take effect", err)
	}
}

func TestAmountChangesInsideWindowValid(t *testing.T) {
	from, until := "2026-10-01", "2027-12-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{{
		Id: "windowed", Name: "windowed", Amount: 90, From: &from, Until: &until,
		AmountChanges: []AmountChange{{From: "2027-01-01", Amount: 100}},
	}}}}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("a change inside the window was refused: %v", err)
	}
}

func TestAmountChangesBadDateRefused(t *testing.T) {
	f := changeFile("rent", []AmountChange{{From: "2027-13-01", Amount: 950}})
	if err := ValidateBudget(f); err == nil {
		t.Fatal("an unparseable change date was accepted")
	}
}
