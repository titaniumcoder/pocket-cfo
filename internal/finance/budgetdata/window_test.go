package budgetdata

import (
	"strings"
	"testing"
)

func windowCategory(id string, from, until *string) Category {
	return Category{Id: id, Name: id, Amount: 180, From: from, Until: until}
}

func TestBoundedWindowValid(t *testing.T) {
	from := "2026-01-01"
	until := "2026-08-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{windowCategory("subs", &from, &until)}}}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWindowOpenEndedEachSideValid(t *testing.T) {
	from := "2026-10-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{windowCategory("starts", &from, nil)}}}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("unexpected error for from-only: %v", err)
	}

	until := "2026-08-01"
	g := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{windowCategory("ends", nil, &until)}}}}
	if err := ValidateBudget(g); err != nil {
		t.Fatalf("unexpected error for until-only: %v", err)
	}
}

func TestWindowWithOneOffDateRefused(t *testing.T) {
	from := "2026-01-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{{Id: "both", Name: "both", Amount: 180, Date: &from, From: &from}}}}}
	err := ValidateBudget(f)
	if err == nil {
		t.Fatal("a category with both a one-off date and a window was accepted")
	}
	if !strings.Contains(err.Error(), "never both") {
		t.Errorf("error = %q, want it to name the date/window conflict", err)
	}

	until := "2026-08-01"
	g := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{{Id: "both-2", Name: "both-2", Amount: 180, Date: &until, Until: &until}}}}}
	if err := ValidateBudget(g); err == nil {
		t.Fatal("a category with both a one-off date and a from/until window was accepted")
	}
}

func TestWindowFromAfterUntilRefused(t *testing.T) {
	inverted := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{windowCategory("inverted", strOr("2026-09-01"), strOr("2026-08-01"))}}}}
	err := ValidateBudget(inverted)
	if err == nil {
		t.Fatal("a window whose from is after its until was accepted")
	}
	if !strings.Contains(err.Error(), "after its until") {
		t.Errorf("error = %q, want it to say from is after until", err)
	}
}

func TestWindowSameMonthBoundaryOK(t *testing.T) {
	bound := "2026-08-01"
	f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{windowCategory("one-month-window", &bound, &bound)}}}}
	if err := ValidateBudget(f); err != nil {
		t.Fatalf("a single-month window was refused: %v", err)
	}
}

func TestWindowUnparseableBoundRefused(t *testing.T) {
	for _, bad := range []string{"2026-13-40", "not-a-date"} {
		f := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{windowCategory("bad", strOr(bad), nil)}}}}
		if err := ValidateBudget(f); err == nil {
			t.Errorf("an unparseable from %q was accepted", bad)
		}
		g := BudgetFile{Groups: []Group{{Name: "A", Categories: []Category{windowCategory("bad-2", nil, strOr(bad))}}}}
		if err := ValidateBudget(g); err == nil {
			t.Errorf("an unparseable until %q was accepted", bad)
		}
	}
}

func strOr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}
