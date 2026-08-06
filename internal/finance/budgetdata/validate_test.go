package budgetdata

import "testing"

func TestValidateBudgetDuplicateNameWithinGroup(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{
			{Name: "Rent", Amount: 1000},
			{Name: "Rent", Amount: 500},
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
		{Name: "A", Categories: []Category{{Name: "Hotel", Amount: 200}}},
		{Name: "B", Categories: []Category{{Name: "Hotel", Amount: 300}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetUnique(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Rent", Amount: 1000}}},
		{Name: "B", Categories: []Category{{Name: "Groceries", Amount: 300}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetRequiresPositiveAmount(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Rent", Amount: 0}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a category with no positive amount")
	}
}

func TestValidateBudgetDatedCategoryRequiresValidDate(t *testing.T) {
	badDate := "2026-13-40"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Desk", Amount: 3000, Date: &badDate}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for an invalid planned date")
	}
}

func TestValidateBudgetDatedCategoryOK(t *testing.T) {
	date := "2026-09-01"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Desk", Amount: 3000, Date: &date}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetCategoryURLRequiresHTTP(t *testing.T) {
	bad := "javascript:alert(1)"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Computer", Amount: 3000, Url: &bad}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a non-http(s) category url")
	}
}

func TestValidateBudgetCategoryURLOK(t *testing.T) {
	url := "https://pcbuild.bg/p-pcb-rigs-apex-rgb-bk-50462"
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Computer", Amount: 3000, Url: &url}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetMinimalAmountExceedsAmountFails(t *testing.T) {
	minAmount := 1500.0
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Restaurants", Amount: 500, MinimalAmount: &minAmount}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a minimal_amount greater than amount")
	}
}

func TestValidateBudgetMinimalAmountZeroOK(t *testing.T) {
	minAmount := 0.0
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Diverse", Amount: 1000, MinimalAmount: &minAmount}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetOverridesInvalidDateFails(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Flight", Amount: 400, Overrides: []Override{{Month: "2026-13-40", Amount: 0}}}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for an invalid overrides month")
	}
}

func TestValidateBudgetOverridesDuplicateMonthFails(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Flight", Amount: 400, Overrides: []Override{
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
		{Name: "A", Categories: []Category{{Name: "Flight", Amount: 400, Overrides: []Override{{Month: "2026-08-01", Amount: -1}}}}},
	}}
	if err := ValidateBudget(f); err == nil {
		t.Fatal("expected an error for a negative overrides amount")
	}
}

func TestValidateBudgetOverridesZeroAmountOK(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Flight", Amount: 400, Overrides: []Override{{Month: "2026-08-01", Amount: 0}}}}},
	}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBudgetOverridesOK(t *testing.T) {
	f := BudgetFile{Groups: []Group{
		{Name: "A", Categories: []Category{{Name: "Flight", Amount: 400, Overrides: []Override{
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
		{Name: "A", Categories: []Category{{Name: "Restaurants", Amount: 500, MinimalAmount: &minAmount}}},
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
