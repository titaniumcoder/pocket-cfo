package budgetdata

import (
	"strings"
	"testing"
)

func TestValidateBudgetDuplicateNameWithinGroup(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{
			{Id: "rent", Name: "Rent", Amount: 1000},
			{Id: "rent-2", Name: "Rent", Amount: 500},
		}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a duplicate category name within the same group")
	}
}

// TestValidateBudgetSameNameAcrossGroupsOK confirms the same category name
// is allowed in different groups — e.g. "Hotel" under both a Vienna trip
// group and a Galati trip group — since a category is always shown nested
// under its own group header, so there's no ambiguity on the page.
func TestValidateBudgetSameNameAcrossGroupsOK(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "hotel", Name: "Hotel", Amount: 200}}},
		{Name: "B", Categories: []Category{{Id: "hotel-2", Name: "Hotel", Amount: 300}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetUnique(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "rent-3", Name: "Rent", Amount: 1000}}},
		{Name: "B", Categories: []Category{{Id: "groceries", Name: "Groceries", Amount: 300}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetRequiresPositiveAmount(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "rent-4", Name: "Rent", Amount: 0}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a category with no positive amount")
	}
}

func TestValidateBudgetDatedCategoryRequiresValidDate(t *testing.T) {
	badDate := "2026-13-40"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "desk", Name: "Desk", Amount: 3000, Date: &badDate}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for an invalid planned date")
	}
}

func TestValidateBudgetDatedCategoryOK(t *testing.T) {
	date := "2026-09-01"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "desk-2", Name: "Desk", Amount: 3000, Date: &date}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetCategoryURLRequiresHTTP(t *testing.T) {
	bad := "javascript:alert(1)"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "computer", Name: "Computer", Amount: 3000, Url: &bad}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a non-http(s) category url")
	}
}

func TestValidateBudgetCategoryURLOK(t *testing.T) {
	url := "https://pcbuild.bg/p-pcb-rigs-apex-rgb-bk-50462"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "computer-2", Name: "Computer", Amount: 3000, Url: &url}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetMinimalAmountExceedsAmountFails(t *testing.T) {
	minAmount := 1500.0
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "restaurants", Name: "Restaurants", Amount: 500, MinimalAmount: &minAmount}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a minimal_amount greater than amount")
	}
}

func TestValidateBudgetMinimalAmountZeroOK(t *testing.T) {
	minAmount := 0.0
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "diverse", Name: "Diverse", Amount: 1000, MinimalAmount: &minAmount}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetOverridesInvalidDateFails(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "flight", Name: "Flight", Amount: 400, Overrides: []Override{{Month: "2026-13-40", Amount: 0}}}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for an invalid overrides month")
	}
}

func TestValidateBudgetOverridesDuplicateMonthFails(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "flight-2", Name: "Flight", Amount: 400, Overrides: []Override{
			{Month: "2026-08-01", Amount: 0},
			{Month: "2026-08-15", Amount: 350},
		}}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a duplicate overrides entry (same month, day is ignored)")
	}
}

func TestValidateBudgetOverridesNegativeAmountFails(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "flight-3", Name: "Flight", Amount: 400, Overrides: []Override{{Month: "2026-08-01", Amount: -1}}}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a negative overrides amount")
	}
}

func TestValidateBudgetOverridesZeroAmountOK(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "flight-4", Name: "Flight", Amount: 400, Overrides: []Override{{Month: "2026-08-01", Amount: 0}}}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetOverridesOK(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "flight-5", Name: "Flight", Amount: 400, Overrides: []Override{
			{Month: "2026-08-01", Amount: 0},
			{Month: "2026-09-01", Amount: 427.42},
		}}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetMinimalAmountEqualToAmountOK(t *testing.T) {
	minAmount := 500.0
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "restaurants-2", Name: "Restaurants", Amount: 500, MinimalAmount: &minAmount}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetLoansUnique(t *testing.T) {
	f := BudgetFile{Loans: []Loan{
		{Name: "Mom", Amount: 650000},
		{Name: "Mom", Amount: 5000},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a duplicate loan name")
	}
}

func TestValidateBudgetLoansOK(t *testing.T) {
	f := BudgetFile{Loans: []Loan{
		{Name: "Mom", Amount: 650000},
		{Name: "Florin", Amount: 5000},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetDuplicateIDAcrossGroups(t *testing.T) {
	// Unlike name, an id must be unique across the whole file: it is what a
	// transaction in data/actuals/ points at, so a shared id would silently
	// merge two categories' recorded spending.
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Id: "shared", Name: "Hotel", Amount: 200}}},
		{Name: "B", Categories: []Category{{Id: "shared", Name: "Flight", Amount: 300}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for the same id in two groups")
	}
}

func TestValidateBudgetMissingID(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Rent", Amount: 1000}}},
	}}
	err := ValidateBudget(f)
	if err == nil {
		t.Fatal("expected an error for a category with no id")
	}
	if !strings.Contains(err.Error(), "no id") {
		t.Errorf("error = %q, want it to name the missing id", err)
	}
}
