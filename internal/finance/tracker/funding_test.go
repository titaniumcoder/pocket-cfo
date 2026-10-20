package tracker

import (
	"context"
	"fmt"
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

	august, _ := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.August}}, date(2026, 8, 15), 0, nil)
	if august.CompanyIncomeCents != 500000 {
		t.Errorf("August company income = %d, want the 5 000 invoiced in July", august.CompanyIncomeCents)
	}
	july, _ := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.July}}, date(2026, 8, 15), 0, nil)
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
	pv, _ := trk.fundingIncome(context.Background(), months, date(2026, 8, 15), 3000, nil)
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

// estimateTracker feeds the funding tests a Toggl whose one tracked hour sits
// on monthDay (YYYY-MM-DD) under project 1, priced by the given projects
// fixture. Everything else is empty: no holidays, no other projects.
func estimateTracker(monthDay, projectsJSON string) *Tracker {
	row := fmt.Sprintf(`[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"%sT09:00:00+00:00"}]}]`, monthDay)
	b := &fakeBackend{
		detailed: func(int) (string, string, string) { return row, "", "" },
		projects: projectsJSON,
		holidays: `[]`,
	}
	client := b.transport()
	return &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
		RateCents:   7500,
		Personal:    testParams(),
	}
}

// TestUninvoicedTrackedMonthIsIncomeTwoMonthsLater: October's tracked hours
// have no invoice yet, so December — two months after the work, where the
// invoice will land — carries their estimate: the tracked amount plus the
// workdays still unbilled at the standard rate.
func TestUninvoicedTrackedMonthIsIncomeTwoMonthsLater(t *testing.T) {
	trk := estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha"}]`)
	trk.Invoiced = ComputeInvoiced(nil)

	pv, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
	if pv.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", pv.Err)
	}
	// 13 workdays remain in October after the 13th, plus the untracked
	// workday today: 14 days × 8 h × 75 = 8 400, on top of the tracked 1 500.
	if pv.CompanyIncomeCents != 990000 {
		t.Errorf("December company income = %d, want the tracked 1 500 plus the 8 400 estimate", pv.CompanyIncomeCents)
	}
	if len(estimates[yearMonth{2026, time.December}]) != 1 {
		t.Fatalf("estimates = %+v, want one estimate for December", estimates)
	}
	if got := estimates[yearMonth{2026, time.December}][0]; got.WorkedMonth != (yearMonth{2026, time.October}) || got.Cents != 990000 {
		t.Errorf("estimate = %+v, want October 2026 at 990000", got)
	}
}

// TestAnInvoiceIssuedInOrBeforeTheWorkedMonthSupersedesItsEstimate: an
// invoice issued in October covers October's work, so nothing of it is
// estimated any more.
func TestAnInvoiceIssuedInOrBeforeTheWorkedMonthSupersedesItsEstimate(t *testing.T) {
	trk := estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha","client_id":7}]`)
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", ClientID: 7, IssueDate: date(2026, 10, 30), DueDate: date(2026, 10, 31), TotalCents: 500000},
	})

	pv, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
	if pv.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", pv.Err)
	}
	if pv.CompanyIncomeCents != 0 {
		t.Errorf("December company income = %d, want 0 — the invoice (income November) covers October's work", pv.CompanyIncomeCents)
	}
	if len(estimates) != 0 {
		t.Errorf("estimates = %+v, want none", estimates)
	}
}

// TestTrackingUntilExtendsCoverageToTheDayItNames: an invoice issued in
// September whose tracking_until names October 15 covers October's work only
// through that day — the estimate keeps the workdays after it.
func TestTrackingUntilExtendsCoverageToTheDayItNames(t *testing.T) {
	trk := estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha","client_id":7}]`)
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", ClientID: 7, IssueDate: date(2026, 9, 30), DueDate: date(2026, 10, 31), TotalCents: 500000, TrackingUntil: date(2026, 10, 15)},
	})

	_, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
	// The 11 workdays from the 16th through the 31st, priced by day.
	if got := estimates[yearMonth{2026, time.December}][0].Cents; got != 660000 {
		t.Errorf("estimate = %d, want 660000 — the workdays after tracking_until, not the tracked month", got)
	}
}

// TestBilledHoursDropOutButTheProjectionStays: one client's October work is
// invoiced whole while the other's has no invoice at all. The billed client's
// tracked hours drop out of December's estimate, and the workdays still
// unbilled keep being projected for the client that is — with one client
// billed and one not, the untracked days belong to whoever is still unbilled,
// so the estimate keeps the same tracked-plus-expected arithmetic the work
// record panel prices.
func TestBilledHoursDropOutButTheProjectionStays(t *testing.T) {
	rows := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-10-05T09:00:00+00:00"}]},
		{"project_id":2,"hourly_rate_in_cents":7500,"billable_amount_in_cents":300000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-10-06T09:00:00+00:00"}]}]`
	trk := estimateTrackerRows(rows, `[{"id":1,"name":"Unbilled","client_id":7},{"id":2,"name":"Billed","client_id":8}]`)
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", ClientID: 8, IssueDate: date(2026, 10, 19), DueDate: date(2026, 11, 2), TotalCents: 500000, TrackingUntil: date(2026, 10, 31)},
	})

	pv, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
	if pv.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", pv.Err)
	}
	// The billed client's 3 000 is out; the unbilled client's 1 500 stays,
	// plus the same 14 workdays × 8 h × 75 = 8 400 projection.
	if got := estimates[yearMonth{2026, time.December}][0].Cents; got != 990000 {
		t.Errorf("estimate = %d, want 990000 — the unbilled 1 500 plus the projection, without the billed 3 000", got)
	}
}

// TestTheProjectionDiesOnlyWhenEveryBilledClientCoversTheMonth: with every
// billable client invoiced through the month's end, the untracked days are
// all billed — nothing is projected any more.
func TestTheProjectionDiesOnlyWhenEveryBilledClientCoversTheMonth(t *testing.T) {
	rows := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-10-05T09:00:00+00:00"}]},
		{"project_id":2,"hourly_rate_in_cents":7500,"billable_amount_in_cents":300000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-10-06T09:00:00+00:00"}]}]`
	trk := estimateTrackerRows(rows, `[{"id":1,"name":"Alpha","client_id":7},{"id":2,"name":"Beta","client_id":8}]`)
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", ClientID: 7, IssueDate: date(2026, 10, 19), DueDate: date(2026, 11, 2), TotalCents: 500000, TrackingUntil: date(2026, 10, 31)},
		{Number: "0002", ClientID: 8, IssueDate: date(2026, 10, 19), DueDate: date(2026, 11, 2), TotalCents: 500000, TrackingUntil: date(2026, 10, 31)},
	})

	_, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
	if len(estimates) != 0 {
		t.Errorf("estimates = %+v, want none — both clients are invoiced through October's end", estimates)
	}
}

// TestATrackingUntilInsideTheIssueMonthLimitsCoverage: an invoice issued in
// October with tracking_until October 10 does not cover the whole month —
// the estimate keeps the fifteen workdays from the 11th on.
func TestATrackingUntilInsideTheIssueMonthLimitsCoverage(t *testing.T) {
	trk := estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha","client_id":7}]`)
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", ClientID: 7, IssueDate: date(2026, 10, 30), DueDate: date(2026, 11, 30), TotalCents: 500000, TrackingUntil: date(2026, 10, 10)},
	})

	_, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
	if got := estimates[yearMonth{2026, time.December}][0].Cents; got != 900000 {
		t.Errorf("estimate = %d, want 900000 — the workdays from the 11th through the 31st", got)
	}
}

// TestFutureMonthsAreEstimatedFromExpectedWorkdays: a worked month with no
// tracking at all is all expectation — its workdays at the standard rate,
// landing as income two months later.
func TestFutureMonthsAreEstimatedFromExpectedWorkdays(t *testing.T) {
	b := &fakeBackend{}
	client := b.transport()
	trk := &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
		RateCents:   7500,
		Personal:    testParams(),
	}

	_, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2027, time.February}}, date(2026, 10, 13), 0, nil)
	// December 2026 holds 23 workdays.
	if got := estimates[yearMonth{2027, time.February}][0].Cents; got != 1380000 {
		t.Errorf("estimate = %d, want 1380000 — December's 23 workdays at 8 h × 75", got)
	}
}

// TestIgnoredClientHoursNeverBecomeEstimates: the own company's client is on
// the ignore list, so its tracked hours price nothing — the month's estimate
// is the expected workdays alone, never the ignored amounts.
func TestIgnoredClientHoursNeverBecomeEstimates(t *testing.T) {
	trk := estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha","client_id":5}]`)
	trk.IgnoredClientIDs = map[int]bool{5: true}
	trk.Invoiced = ComputeInvoiced(nil)

	_, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
	if got := estimates[yearMonth{2026, time.December}][0].Cents; got != 840000 {
		t.Errorf("estimate = %d, want 840000 — the expected workdays without the ignored 1 500", got)
	}
}

// TestATogglFailureKeepsInvoicedIncomeAndDropsEstimates: when Toggl cannot
// answer, the cascade falls back to the real invoices and says so instead of
// showing a wrong number.
func TestATogglFailureKeepsInvoicedIncomeAndDropsEstimates(t *testing.T) {
	b := &fakeBackend{failDetailed: 500, projects: `[{"id":1,"name":"Alpha"}]`}
	client := b.transport()
	trk := &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
		RateCents:   7500,
		Personal:    testParams(),
		Invoiced: ComputeInvoiced([]InvoicedFact{
			{Number: "0001", IssueDate: date(2026, 9, 30), DueDate: date(2026, 10, 31), TotalCents: 500000},
		}),
	}

	pv, estimates := trk.fundingIncome(context.Background(), []yearMonth{{2026, time.November}}, date(2026, 10, 13), 0, nil)
	if pv.Err == "" {
		t.Fatal("FundingPersonal.Err is empty — a Toggl failure must say so")
	}
	if pv.CompanyIncomeCents != 500000 {
		t.Errorf("company income = %d, want the invoice's 5 000 alone", pv.CompanyIncomeCents)
	}
	if len(estimates) != 0 {
		t.Errorf("estimates = %+v, want none", estimates)
	}
}

// estimateTrackerRows is estimateTracker with the full detailed-report body,
// so a test can lay down several projects' worth of tracked hours.
func estimateTrackerRows(rowsJSON, projectsJSON string) *Tracker {
	b := &fakeBackend{
		detailed: func(int) (string, string, string) { return rowsJSON, "", "" },
		projects: projectsJSON,
		holidays: `[]`,
	}
	client := b.transport()
	return &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
		RateCents:   7500,
		Personal:    testParams(),
	}
}

// TestCoveredHoursDropOutOfTheEstimatedIncomeTotal: work an issued invoice
// already covers is no longer estimated income — the panel's Income total
// prices only what no invoice exists for yet.
func TestCoveredHoursDropOutOfTheEstimatedIncomeTotal(t *testing.T) {
	ctx := context.Background()

	newTracker := func(invoiced []InvoicedFact) *Tracker {
		trk := estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha","client_id":7}]`)
		trk.Invoiced = ComputeInvoiced(invoiced)
		return trk
	}

	figure := func(trk *Tracker) *Figures {
		f := &Figures{}
		yd, err := trk.hours().Year(ctx, 2026)
		if err != nil {
			t.Fatal(err)
		}
		projects, err := trk.hours().Projects(ctx)
		if err != nil {
			t.Fatal(err)
		}
		aggs := aggregatesInRange(yd, date(2026, 10, 1), date(2026, 10, 31))
		hours, cents := f.computeTrackedRows(trk, projects, aggs, yd, nil, nil)
		unbilledCents, unbilledHours := f.computeUnbilled(trk, projects, yd, 2026, date(2026, 10, 1), date(2026, 10, 31))
		f.computeTotal(hours, cents, 0, true, trk.RateCents, unbilledCents, unbilledHours)
		return f
	}

	covered := figure(newTracker([]InvoicedFact{
		{Number: "0001", ClientID: 7, IssueDate: date(2026, 10, 30), DueDate: date(2026, 11, 30), TotalCents: 500000},
	}))
	if covered.TotalCents != 0 {
		t.Errorf("Income total = %d, want 0 — October's 1 500 is already invoiced", covered.TotalCents)
	}

	uncovered := figure(newTracker(nil))
	if uncovered.TotalCents != 150000 {
		t.Errorf("Income total = %d, want 1 500 — nothing covers October's work", uncovered.TotalCents)
	}
}

// TestARealInvoiceReplacesTheEstimateRowInItsIncomeMonth: while October's
// work has no invoice, December's rows carry the estimate for it; once the
// invoice is issued, the estimate is gone and the real invoice stands.
func TestARealInvoiceReplacesTheEstimateRowInItsIncomeMonth(t *testing.T) {
	ctx := context.Background()

	rows := func(trk *Tracker) (realAndEstimated, estimates []InvoicedRow) {
		f := &Figures{}
		f.FundingPersonal, f.Estimated = trk.fundingIncome(ctx, []yearMonth{{2026, time.December}}, date(2026, 10, 13), 0, nil)
		f.computeInvoicedRows(trk, 2026, date(2026, 12, 1), date(2026, 12, 31))
		return f.Invoiced, f.ExpectedEstimates
	}

	uninvoiced, estimates := rows(estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha"}]`))
	if len(uninvoiced) != 1 || !uninvoiced[0].Estimated || uninvoiced[0].WorkedMonth != "October 2026" {
		t.Fatalf("rows = %+v, want one estimate for October 2026", uninvoiced)
	}
	if len(estimates) != 1 || estimates[0].WorkedMonth != "October 2026" {
		t.Errorf("ExpectedEstimates = %+v, want the same estimate listed under Expected", estimates)
	}

	invoicedTrk := estimateTracker("2026-10-05", `[{"id":1,"name":"Alpha","client_id":7}]`)
	invoicedTrk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{Number: "0001", ClientID: 7, IssueDate: date(2026, 10, 30), DueDate: date(2026, 11, 30), TotalCents: 500000},
	})
	invoiced, estimates := rows(invoicedTrk)
	if len(invoiced) != 1 {
		t.Fatalf("rows = %+v, want exactly the real invoice", invoiced)
	}
	if invoiced[0].Estimated || invoiced[0].Number != "0001" {
		t.Errorf("row = %+v, want the real invoice 0001 with no estimate beside it", invoiced[0])
	}
	if len(estimates) != 0 {
		t.Errorf("ExpectedEstimates = %+v, want none — the worked month is invoiced", estimates)
	}
}

// TestUnlinkedProjectsAreMarkedAndIgnoredOnesAreNot: a Toggl client no
// recipient links is flagged as a likely mistake, an ignored client shows
// neutrally, and a linked one carries no marker at all.
func TestUnlinkedProjectsAreMarkedAndIgnoredOnesAreNot(t *testing.T) {
	rows := "[" + strings.Join([]string{
		`{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-10-05T09:00:00+00:00"}]}`,
		`{"project_id":2,"hourly_rate_in_cents":7500,"billable_amount_in_cents":140000,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-10-06T09:00:00+00:00"}]}`,
		`{"project_id":3,"hourly_rate_in_cents":7500,"billable_amount_in_cents":130000,"currency":"EUR","time_entries":[{"seconds":1800,"start":"2026-10-07T09:00:00+00:00"}]}`,
	}, ",") + "]"
	trk := estimateTrackerRows(rows, `[{"id":1,"name":"Linked","client_id":7},{"id":2,"name":"Ignored","client_id":5},{"id":3,"name":"Unlinked"}]`)
	trk.LinkedClientIDs = map[int]bool{7: true}
	trk.IgnoredClientIDs = map[int]bool{5: true}

	f := &Figures{}
	yd, err := trk.hours().Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := trk.hours().Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.computeTrackedRows(trk, projects, aggregatesInRange(yd, date(2026, 10, 1), date(2026, 10, 31)), yd, nil, nil)
	if len(f.Tracked) != 3 {
		t.Fatalf("rows = %+v, want 3", f.Tracked)
	}
	byName := map[string]TrackedRow{}
	for _, r := range f.Tracked {
		byName[r.Project] = r
	}
	if byName["Linked"].Unlinked || byName["Linked"].Ignored {
		t.Errorf("Linked = %+v, want no marker", byName["Linked"])
	}
	if !byName["Ignored"].Ignored || byName["Ignored"].Unlinked {
		t.Errorf("Ignored = %+v, want the neutral not-invoiced marker", byName["Ignored"])
	}
	if !byName["Unlinked"].Unlinked || byName["Unlinked"].Ignored {
		t.Errorf("Unlinked = %+v, want the no-recipient warning", byName["Unlinked"])
	}
}
