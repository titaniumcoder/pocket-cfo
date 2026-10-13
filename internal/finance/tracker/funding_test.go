package tracker

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestYearMonths(t *testing.T) {
	tests := []struct {
		name       string
		start, end yearMonth
		wantMonths int
		wantFirst  yearMonth
		wantLast   yearMonth
	}{
		{"single month", yearMonth{2026, time.May}, yearMonth{2026, time.May}, 1, yearMonth{2026, time.May}, yearMonth{2026, time.May}},
		{"multi month same year", yearMonth{2026, time.May}, yearMonth{2026, time.October}, 6, yearMonth{2026, time.May}, yearMonth{2026, time.October}},
		{"crosses year boundary", yearMonth{2025, time.November}, yearMonth{2026, time.February}, 4, yearMonth{2025, time.November}, yearMonth{2026, time.February}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := yearMonths(tt.start, tt.end)
			if len(got) != tt.wantMonths {
				t.Fatalf("len = %d, want %d (%+v)", len(got), tt.wantMonths, got)
			}
			if got[0] != tt.wantFirst {
				t.Errorf("first = %+v, want %+v", got[0], tt.wantFirst)
			}
			if got[len(got)-1] != tt.wantLast {
				t.Errorf("last = %+v, want %+v", got[len(got)-1], tt.wantLast)
			}
		})
	}
}

// TestFundingIncomeIsTheMonthAfterDue pins the funding rule: a month's
// company income is the invoices that fell due the month before — nothing
// else, and nothing in any other month.
func TestFundingIncomeIsTheMonthAfterDue(t *testing.T) {
	trk := &Tracker{Personal: testParams()}
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", IssueDate: date(2026, 7, 5), DueDate: date(2026, 7, 19), TotalCents: 500000},
	})

	august := trk.fundingIncome([]yearMonth{{2026, time.August}}, 0, nil, companyStock{})
	if august.CompanyIncomeCents != 500000 {
		t.Errorf("August company income = %d, want the 5 000 invoiced in July", august.CompanyIncomeCents)
	}
	july := trk.fundingIncome([]yearMonth{{2026, time.July}}, 0, nil, companyStock{})
	if july.CompanyIncomeCents != 0 {
		t.Errorf("July company income = %d, want 0 — the invoice is not due until July", july.CompanyIncomeCents)
	}
}

// TestFundingIncomeSplitsCompanyExpensesAcrossTheMonths checks the year
// view's expense spreading: the plan's company total is divided evenly over
// the months the cascade covers.
func TestFundingIncomeSplitsCompanyExpensesAcrossTheMonths(t *testing.T) {
	trk := &Tracker{Personal: testParams()}
	months := []yearMonth{{2026, time.January}, {2026, time.February}, {2026, time.March}}
	pv := trk.fundingIncome(months, 3000, nil, companyStock{})
	if pv.CompanyExpensesCents != 300000 {
		t.Errorf("CompanyExpensesCents = %d, want 3 × 1 000", pv.CompanyExpensesCents)
	}
}

// TestBalanceUsesFundingPersonalNetIncome pins Tracker.compute's balance
// formula to the cascade the page renders, not some parallel figure.
func TestBalanceUsesFundingPersonalNetIncome(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", IssueDate: date(2026, 7, 5), DueDate: date(2026, 7, 19), TotalCents: 500000},
	})

	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.FundingPersonal.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", f.FundingPersonal.Err)
	}
	if f.FundingPersonal.CompanyIncomeCents != 500000 {
		t.Fatalf("company income = %d, want the invoice's 5 000", f.FundingPersonal.CompanyIncomeCents)
	}
	want := f.FundingPersonal.NetIncomeCents - f.PrivateTotalPlannedCents
	if f.BalanceCents != want {
		t.Errorf("BalanceCents = %d, want %d (FundingPersonal.NetIncomeCents - PrivateTotalPlannedCents)", f.BalanceCents, want)
	}
}

// testBudgetJSONCompanyDated has one company-kind one-off cost, "Computer",
// due 2026-09-01 — used to pin down that a company expense's due date is
// evaluated against the VIEWED month.
const testBudgetJSONCompanyDated = `{
  "groups": [
    { "name": "Company - Equipment", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000012", "name": "Computer", "amount": 3000, "date": "2026-09-01" }
    ]}
  ]
}`

// TestFundingPersonalCompanyExpenseStaysOnViewedPeriod: company expenses are
// a real-time business fact, tied to whichever month is actually being
// browsed.
func TestFundingPersonalCompanyExpenseStaysOnViewedPeriod(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSONCompanyDated})

	// Viewing September 2026 — the category's own due month — directly
	// should show it as a real, spent line, not a future reminder.
	sep := trk.ComputeMonth(context.Background(), 2026, time.September)
	if sep.FundingPersonal.Err != "" {
		t.Fatalf("viewing September: FundingPersonal.Err = %q", sep.FundingPersonal.Err)
	}
	sepRow := rowByName(BudgetView{Groups: sep.FundingPersonal.CompanyGroups}, "Computer")
	if sepRow.PlannedCents != eurToCents(3000) {
		t.Errorf("viewing September: Computer.PlannedCents = %d, want %d", sepRow.PlannedCents, eurToCents(3000))
	}
	if sepRow.UpcomingMonth != "" {
		t.Errorf("viewing September: Computer should not still be a future reminder, got UpcomingMonth = %q", sepRow.UpcomingMonth)
	}

	// Viewing July 2026 — before its due date — should show it
	// as an upcoming reminder.
	jul := trk.ComputeMonth(context.Background(), 2026, time.July)
	if jul.FundingPersonal.Err != "" {
		t.Fatalf("viewing July: FundingPersonal.Err = %q", jul.FundingPersonal.Err)
	}
	julRow := rowByName(BudgetView{Groups: jul.FundingPersonal.CompanyGroups}, "Computer")
	if julRow.UpcomingMonth != "September 2026" {
		t.Errorf("viewing July: Computer.UpcomingMonth = %q, want %q", julRow.UpcomingMonth, "September 2026")
	}

	// Viewing November 2026 — after its due month has passed — should drop
	// it entirely, same as any past one-off cost (see categoryRowFor).
	nov := trk.ComputeMonth(context.Background(), 2026, time.November)
	if nov.FundingPersonal.Err != "" {
		t.Fatalf("viewing November: FundingPersonal.Err = %q", nov.FundingPersonal.Err)
	}
	if len(nov.FundingPersonal.CompanyGroups) != 0 {
		t.Errorf("viewing November: CompanyGroups = %+v, want empty (Computer's due month has already passed)", nov.FundingPersonal.CompanyGroups)
	}
}

// TestRenderPageTwoPanelsAndLinks confirms the dashboard actually renders as
// two distinct panels, and that neither panel leaks the other's figures.
func TestRenderPageTwoPanelsAndLinks(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	f := trk.ComputeMonth(context.Background(), 2026, time.March)

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	if !strings.Contains(body, `class="panel income-panel"`) {
		t.Error("missing income panel")
	}
	if !strings.Contains(body, `class="panel budget-panel"`) {
		t.Error("missing rolling-budget panel")
	}

	incomePanel := body[strings.Index(body, `class="panel income-panel"`):strings.Index(body, `class="panel budget-panel"`)]
	expensesPanel := body[strings.Index(body, `class="panel budget-panel"`):]

	// Income panel: Tracked + Expected, ending in an "Income" total row —
	// no Company-income/salary cascade here at all.
	if !strings.Contains(incomePanel, `<span class="label">Income</span>`) {
		t.Error("income panel missing the Income total row")
	}
	if strings.Contains(incomePanel, "Personal income (Bulgaria)") {
		t.Error("the salary cascade should not render in the income panel")
	}

	// Expenses panel: "Company income" stands on its own — no shifted
	// funding period to link to any more.
	if !strings.Contains(expensesPanel, "Personal income (Bulgaria)") {
		t.Error("expenses panel missing the Personal income (Bulgaria) cascade")
	}
	if strings.Contains(expensesPanel, `class="period-link"`) {
		t.Errorf("expenses panel still carries a funding-period link: %s", expensesPanel)
	}
}
