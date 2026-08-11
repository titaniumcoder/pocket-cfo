package tracker

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMonthsBetween(t *testing.T) {
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
			got := monthsBetween(tt.start, tt.end)
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

func TestFundingRangeForMonth(t *testing.T) {
	tests := []struct {
		name      string
		year      int
		month     time.Month
		wantStart yearMonth
	}{
		{"mid-year, no crossing", 2026, time.July, yearMonth{2026, time.May}},
		{"January crosses into previous year", 2026, time.January, yearMonth{2025, time.November}},
		{"February crosses into previous year", 2026, time.February, yearMonth{2025, time.December}},
		{"March, no crossing", 2026, time.March, yearMonth{2026, time.January}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := fundingRangeForMonth(tt.year, tt.month)
			if start != tt.wantStart || end != tt.wantStart {
				t.Errorf("fundingRangeForMonth(%d, %s) = %+v..%+v, want %+v (single month)", tt.year, tt.month, start, end, tt.wantStart)
			}
		})
	}
}

func TestFundingRangeForYearSameYear(t *testing.T) {
	// Viewing the current year, testNow is mid-year (July) -> expense range
	// is July-December (unaffected by this change), so funding range is the
	// same shifted back two months: May-October, no year crossing.
	start, end := fundingRangeForYear(2026, testNow)
	if start != (yearMonth{2026, time.May}) {
		t.Errorf("start = %+v, want May 2026", start)
	}
	if end != (yearMonth{2026, time.October}) {
		t.Errorf("end = %+v, want October 2026", end)
	}
}

func TestFundingRangeForYearCrossesIntoPreviousYear(t *testing.T) {
	// Viewing the current year with "now" in February -> expense range is
	// February-December, so the funding range's start (Feb - 2 = December)
	// lands in the previous calendar year.
	now := time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC)
	start, end := fundingRangeForYear(2026, now)
	if start != (yearMonth{2025, time.December}) {
		t.Errorf("start = %+v, want December 2025", start)
	}
	if end != (yearMonth{2026, time.October}) {
		t.Errorf("end = %+v, want October 2026", end)
	}

	// "now" in January -> expense range is January-December (the full
	// year), so the funding start (Jan - 2 = November) lands even further
	// back in the previous year.
	nowJan := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	start2, _ := fundingRangeForYear(2026, nowJan)
	if start2 != (yearMonth{2025, time.November}) {
		t.Errorf("start (Jan) = %+v, want November 2025", start2)
	}
}

func TestFundingRangeForYearOtherViewedYear(t *testing.T) {
	// A year other than testNow's own is entirely "remaining" relative to
	// itself (see Budget.ForYear/privateExpenseStartMonth), so the expense
	// range is always the full January-December, and the funding range is
	// always November(year-1)-October(year), regardless of testNow.
	pastStart, pastEnd := fundingRangeForYear(2024, testNow)
	if pastStart != (yearMonth{2023, time.November}) || pastEnd != (yearMonth{2024, time.October}) {
		t.Errorf("past year range = %+v..%+v, want Nov 2023..Oct 2024", pastStart, pastEnd)
	}

	futureStart, futureEnd := fundingRangeForYear(2028, testNow)
	if futureStart != (yearMonth{2027, time.November}) || futureEnd != (yearMonth{2028, time.October}) {
		t.Errorf("future year range = %+v..%+v, want Nov 2027..Oct 2028", futureStart, futureEnd)
	}
}

func TestSpendRangeForMonth(t *testing.T) {
	tests := []struct {
		name      string
		year      int
		month     time.Month
		wantMonth yearMonth
	}{
		{"mid-year, no crossing", 2026, time.July, yearMonth{2026, time.September}},
		{"November crosses into next year", 2026, time.November, yearMonth{2027, time.January}},
		{"December crosses into next year", 2026, time.December, yearMonth{2027, time.February}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := spendRangeForMonth(tt.year, tt.month)
			if start != tt.wantMonth || end != tt.wantMonth {
				t.Errorf("spendRangeForMonth(%d, %s) = %+v..%+v, want %+v", tt.year, tt.month, start, end, tt.wantMonth)
			}
		})
	}
}

func TestSpendRangeForYear(t *testing.T) {
	// Personal income in year view always spans the full viewed year
	// (Jan-Dec), so its spendable range always crosses into the next
	// calendar year at the end: Jan+2..Dec+2 = March(year)..February(year+1).
	start, end := spendRangeForYear(2026)
	if start != (yearMonth{2026, time.March}) {
		t.Errorf("start = %+v, want March 2026", start)
	}
	if end != (yearMonth{2027, time.February}) {
		t.Errorf("end = %+v, want February 2027", end)
	}
}

func TestLinkForRangeSingleMonth(t *testing.T) {
	got := linkForRange(yearMonth{2026, time.May}, yearMonth{2026, time.May}, 2026)
	if got != "/2026/5" {
		t.Errorf("linkForRange = %q, want /2026/5", got)
	}
}

func TestLinkForRangeMultiMonthUsesMajorityYear(t *testing.T) {
	got := linkForRange(yearMonth{2025, time.November}, yearMonth{2026, time.October}, 2026)
	if got != "/2026" {
		t.Errorf("linkForRange = %q, want /2026", got)
	}
}

func TestFundingIncomeDegradesOnTogglError(t *testing.T) {
	b := &fakeBackend{failDetailed: 500}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}
	trk := &Tracker{
		Toggl: tg, Holidays: &Holidays{HTTP: b.transport()}, Loc: time.UTC,
		HoursPerDay: 8,
		Personal:    PersonalParams{EmployerRate: 0.1892, EmployeeRate: 0.1378, MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10},
	}
	pv := trk.fundingIncome(context.Background(), yearMonth{2026, time.January}, yearMonth{2026, time.January}, mar(15), 7500, 0, nil)
	if pv.Err == "" {
		t.Fatal("expected Err to be set when the Toggl fetch fails")
	}
}

// TestBalanceUsesFundingPersonalNotViewedPeriodPersonal is a regression test
// pinning Tracker.compute's Balance formula to FundingPersonal.NetIncomeCents
// (the shifted period), not Personal.NetIncomeCents (the viewed period —
// still computed, but only for the JSON API now, see web.go). Both January
// and March rows come back from the same single yearly fetch
// (Toggl.Year(2026) — the viewed month, March, and its funding month,
// January, both fall in 2026) and are bucketed into their real months by
// Toggl.fetchYear regardless of which request range fetched them, so one
// fake response with genuinely different January/March amounts is enough to
// make a regression back to the old formula show up as a numeric mismatch,
// not a coincidental zero.
func TestBalanceUsesFundingPersonalNotViewedPeriodPersonal(t *testing.T) {
	rows := `[
		{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-03-02T09:00:00+00:00"}]},
		{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":50000,"currency":"EUR","time_entries":[{"seconds":2400,"start":"2026-01-05T09:00:00+00:00"}]}
	]`
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return rows, "", "" },
		projects: `[{"id":1,"name":"Alpha"}]`,
	}
	client := b.transport()
	trk := &Tracker{
		Toggl: &Toggl{WorkspaceID: "ws", HTTP: client}, Holidays: &Holidays{HTTP: client},
		HoursPerDay: 8, Loc: time.UTC,
		Personal: PersonalParams{EmployerRate: 0.1892, EmployeeRate: 0.1378, MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10},
	}
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})

	f := trk.ComputeMonth(context.Background(), 2026, time.March)
	if f.FundingPersonal.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", f.FundingPersonal.Err)
	}
	if f.Personal.Err != "" {
		t.Fatalf("Personal.Err = %q", f.Personal.Err)
	}
	if f.FundingPersonal.NetIncomeCents == f.Personal.NetIncomeCents {
		t.Fatalf("FundingPersonal.NetIncomeCents (%d) should differ from Personal.NetIncomeCents (%d) given distinct January/March fixtures", f.FundingPersonal.NetIncomeCents, f.Personal.NetIncomeCents)
	}
	want := f.FundingPersonal.NetIncomeCents - f.PrivateTotalPlannedCents
	if f.BalanceCents != want {
		t.Errorf("BalanceCents = %d, want %d (FundingPersonal.NetIncomeCents - PrivateTotalPlannedCents)", f.BalanceCents, want)
	}
	if got := f.Personal.NetIncomeCents - f.PrivateTotalPlannedCents; f.BalanceCents == got {
		t.Errorf("BalanceCents (%d) must not equal the viewed-period-based formula (%d)", f.BalanceCents, got)
	}
}

// TestFundingIncomeCrossesYearBoundary confirms the funding pipeline
// actually reaches into the previous calendar year's Toggl data when the
// viewed month is January or February — the case fundingRangeForMonth
// documents but nothing else in the test suite exercises end to end.
// detailedForRange gives 2025's yearly fetch genuinely different tracked
// data (both a March 2025 and a November 2025 entry) from 2026's, so a
// nonzero result here can only come from the cross-year fetch actually
// working, not from a coincidental shared fixture.
func TestFundingIncomeCrossesYearBoundary(t *testing.T) {
	const marchAmount = 150000
	const novemberAmount = 80000
	b := &fakeBackend{
		detailedForRange: func(startDate, endDate string) (string, string, string) {
			if strings.HasPrefix(startDate, "2025-") {
				return fmt.Sprintf(`[
					{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":%d,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2025-03-02T09:00:00+00:00"}]},
					{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":%d,"currency":"EUR","time_entries":[{"seconds":3200,"start":"2025-11-10T09:00:00+00:00"}]}
				]`, marchAmount, novemberAmount), "", ""
			}
			return fmt.Sprintf(`[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":%d,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-03-02T09:00:00+00:00"}]}]`, marchAmount), "", ""
		},
		projects: `[{"id":1,"name":"Alpha"}]`,
	}
	client := b.transport()
	trk := &Tracker{
		Toggl: &Toggl{WorkspaceID: "ws", HTTP: client}, Holidays: &Holidays{HTTP: client},
		HoursPerDay: 8, Loc: time.UTC,
		Personal: PersonalParams{EmployerRate: 0.1892, EmployeeRate: 0.1378, MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10},
	}
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})

	f := trk.ComputeMonth(context.Background(), 2026, time.January)
	if f.FundingPersonal.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", f.FundingPersonal.Err)
	}
	if want := "November 2025"; f.FundingPersonal.FundingLabel != want {
		t.Errorf("FundingPersonal.FundingLabel = %q, want %q", f.FundingPersonal.FundingLabel, want)
	}
	if want := "/2025/11"; f.FundingPersonal.FundingURL != want {
		t.Errorf("FundingPersonal.FundingURL = %q, want %q", f.FundingPersonal.FundingURL, want)
	}
	if f.FundingPersonal.NetIncomeCents <= 0 {
		t.Errorf("FundingPersonal.NetIncomeCents = %d, want > 0 (November 2025 has tracked data in this fixture)", f.FundingPersonal.NetIncomeCents)
	}
}

// TestFundingIncomeInvoicedUsesCorrectMonthNotDoubleShifted is a regression
// test for a real bug: monthLaborIncome looked up invoicedCentsForMonth at
// the already-shifted funding month instead of the viewed month it funds,
// double-applying fundingShiftMonths on top of the invoice's own
// due-date-derived usable month. An invoice due 2026-08-31 is usable in
// September 2026 (due date + 1 month, see invoiced.go); the fix must make it
// show up when VIEWING September (funding month = July, July+2 = September)
// and NOT when viewing November (funding month = September; the buggy code
// looked up invoicedCentsForMonth(September) directly there too, wrongly
// finding the same September-keyed entry a second time).
func TestFundingIncomeInvoicedUsesCorrectMonthNotDoubleShifted(t *testing.T) {
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return `[]`, "", "" },
		projects: `[{"id":1,"name":"Alpha","client_id":1}]`,
	}
	client := b.transport()
	newTrk := func() *Tracker {
		return &Tracker{
			Toggl: &Toggl{WorkspaceID: "ws", HTTP: client}, Holidays: &Holidays{HTTP: client},
			HoursPerDay: 8, Loc: time.UTC, RateCents: 7500, RateCurrency: "EUR",
			Personal: PersonalParams{EmployerRate: 0.1892, EmployeeRate: 0.1378, MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10},
		}
	}
	invoiced := ComputeInvoiced([]InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 31), TotalCents: 500000},
	})

	before := newTrk().ComputeMonth(context.Background(), 2026, time.September)
	withInvoice := newTrk()
	withInvoice.Invoiced = invoiced
	after := withInvoice.ComputeMonth(context.Background(), 2026, time.September)
	if after.FundingPersonal.NetIncomeCents <= before.FundingPersonal.NetIncomeCents {
		t.Errorf("viewing September: FundingPersonal.NetIncomeCents with invoice (%d) should exceed without (%d) — the invoice becomes usable in September",
			after.FundingPersonal.NetIncomeCents, before.FundingPersonal.NetIncomeCents)
	}

	beforeNov := newTrk().ComputeMonth(context.Background(), 2026, time.November)
	withInvoiceNov := newTrk()
	withInvoiceNov.Invoiced = invoiced
	afterNov := withInvoiceNov.ComputeMonth(context.Background(), 2026, time.November)
	if afterNov.FundingPersonal.NetIncomeCents != beforeNov.FundingPersonal.NetIncomeCents {
		t.Errorf("viewing November: FundingPersonal.NetIncomeCents changed (%d -> %d) even though the invoice isn't usable until September (bug: double-shifted two months late)",
			beforeNov.FundingPersonal.NetIncomeCents, afterNov.FundingPersonal.NetIncomeCents)
	}
}

// testBudgetJSONCompanyDated has one company-kind one-off cost, "Computer",
// due 2026-09-01 — used to pin down that a company expense's due date is
// evaluated against the VIEWED period, never the shifted funding period.
const testBudgetJSONCompanyDated = `{
  "groups": [
    { "name": "Company - Equipment", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000012", "name": "Computer", "amount": 3000, "date": "2026-09-01" }
    ]}
  ]
}`

// TestFundingPersonalCompanyExpenseStaysOnViewedPeriod is a regression test
// for a real bug: company-kind expense categories were being evaluated
// against the shifted funding period, so a cost due 2026-09-01 only showed
// as a real (non-planned) line item when browsing to November 2026 (Sept +
// 2) instead of September 2026 itself — effectively postponing a fixed
// budget.json date by two months. Company expenses are a real-time business
// fact, not subject to the payroll lag that motivates the income shift (see
// Tracker.fundingIncome), so they must behave exactly as they did before
// this feature existed: tied to whichever month is actually being browsed.
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
		t.Errorf("viewing September: Computer.PlannedCents = %d, want %d (real spend in its own due month, not postponed to November)", sepRow.PlannedCents, eurToCents(3000))
	}
	if sepRow.UpcomingMonth != "" {
		t.Errorf("viewing September: Computer should not still be a future reminder, got UpcomingMonth = %q", sepRow.UpcomingMonth)
	}

	// Viewing July 2026 — two months before its due date — should show it
	// as an upcoming reminder, same as before this feature existed.
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
// two distinct panels, and that the funding-income label (Expenses panel)
// and the "Usable from" label (Income panel) each render as a link to the
// right period — and that neither label leaks into the other panel.
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
	if !strings.Contains(incomePanel, `<span class="label">Income `) {
		t.Error("income panel missing the Income total row")
	}
	if strings.Contains(incomePanel, "Personal income (Bulgaria)") {
		t.Error("the salary cascade should not render in the income panel")
	}

	// Expenses panel: "Company income" is annotated with, and links to, the
	// funding period (January 2026, March minus two) — not March itself.
	if !strings.Contains(expensesPanel, "Personal income (Bulgaria)") {
		t.Error("expenses panel missing the Personal income (Bulgaria) cascade")
	}
	if !strings.Contains(expensesPanel, `href="/2026/1"`) || !strings.Contains(expensesPanel, "January 2026") {
		t.Errorf("expenses panel missing the funding link/label: %s", expensesPanel)
	}
	if strings.Contains(incomePanel, "January 2026") {
		t.Error("funding label should not be duplicated into the income panel")
	}

	// Spendable label ("May 2026", the viewed month plus two, inline as
	// "Income (for May 2026)") belongs only in the Income panel, as a link
	// forward to it.
	if !strings.Contains(incomePanel, `(for <a class="period-link" href="/2026/5">May 2026</a>)`) {
		t.Errorf("income panel missing the spendable link/label: %s", incomePanel)
	}
	if strings.Contains(expensesPanel, ">May 2026</a>") {
		t.Error("spendable label should not be duplicated into the expenses panel")
	}
}
