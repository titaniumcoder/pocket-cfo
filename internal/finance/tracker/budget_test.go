package tracker

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

const testBudgetJSON = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 1000 },
      { "id": "00000000-0000-4000-8000-000000000002", "name": "Groceries", "amount": 300 }
    ]},
    { "name": "OneOff", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000003", "name": "Desk", "amount": 500, "date": "2026-09-01" }
    ]}
  ],
  "loans": [
    { "name": "Mom", "amount": 650000 }
  ]
}`

// testBudgetJSONWithCompany extends testBudgetJSON's shape with a
// company-kind group, for tests specifically about the company/private
// split (see BudgetView.CompanyGroups/CompanyTotalPlannedCents).
const testBudgetJSONWithCompany = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 1000 }
    ]},
    { "name": "Office", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000004", "name": "Accounting", "amount": 200 }
    ]}
  ]
}`

// testNow is a fixed "current time" for tests about a dated category's
// visibility — anything before Desk's 2026-09-01 date keeps it visible (as
// "future") rather than hidden.
var testNow = time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)

// newTestBudget returns a Budget reading from an in-memory fstest.MapFS
// (keyed by path relative to internal/data, e.g. "budget.json") — Budget's
// reads come from FS, not GitHub (see internal/tracker/budget.go), so no
// fake HTTP transport is needed here.
func newTestBudget(t *testing.T, files map[string]string) *Budget {
	t.Helper()
	mfs := fstest.MapFS{}
	for path, content := range files {
		mfs[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return &Budget{FS: mfs}
}

func TestBudgetFileFetchAndValidate(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})

	bf, err := b.File(context.Background())
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if len(bf.Groups) != 2 || bf.Groups[0].Categories[0].Name != "Rent" {
		t.Errorf("unexpected groups: %+v", bf)
	}
}

func TestBudgetFileMissing(t *testing.T) {
	b := newTestBudget(t, nil)
	if _, err := b.File(context.Background()); err == nil {
		t.Fatal("expected an error when budget.json is missing")
	}
}

func TestBudgetForMonthRecurringCategoryAlwaysCounts(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	view, err := b.ForMonth(context.Background(), 2026, time.January, testNow)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	byName := map[string]CategoryRow{}
	for _, g := range view.Groups {
		for _, r := range g.Rows {
			byName[r.Name] = r
		}
	}
	if want := eurToCents(1000); byName["Rent"].PlannedCents != want {
		t.Errorf("Rent spent = %d, want %d", byName["Rent"].PlannedCents, want)
	}
	if want := eurToCents(300); byName["Groceries"].PlannedCents != want {
		t.Errorf("Groceries spent = %d, want %d", byName["Groceries"].PlannedCents, want)
	}
}

func TestBudgetForMonthDatedCategoryCountsOnlyWhenDue(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})

	due, err := b.ForMonth(context.Background(), 2026, time.September, testNow)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if got := rowByName(due, "Desk").PlannedCents; got != eurToCents(500) {
		t.Errorf("Desk spent in its due month = %d, want %d", got, eurToCents(500))
	}

	notDue, err := b.ForMonth(context.Background(), 2026, time.July, testNow)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if got := rowByName(notDue, "Desk").PlannedCents; got != 0 {
		t.Errorf("Desk spent outside its due month = %d, want 0", got)
	}
}

// TestBudgetForYearCurrentYearOnlyCountsRemainingMonths confirms the year
// view for the year "now" falls in only sums from now's month through
// December — testNow is July 15, 2026, so 2026 should be 6 months
// (Jul-Dec), not 12; months already gone by shouldn't be assumed to still be
// coming, same spirit as a past one-time category being hidden in month view.
func TestBudgetForYearCurrentYearOnlyCountsRemainingMonths(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	view, err := b.ForYear(context.Background(), 2026, testNow, time.Time{})
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if want := eurToCents(6 * 1000); rowByName(view, "Rent").PlannedCents != want {
		t.Errorf("Rent PlannedCents = %d, want %d (Jul-Dec only)", rowByName(view, "Rent").PlannedCents, want)
	}
	if want := eurToCents(6 * 300); rowByName(view, "Groceries").PlannedCents != want {
		t.Errorf("Groceries PlannedCents = %d, want %d (Jul-Dec only)", rowByName(view, "Groceries").PlannedCents, want)
	}
	// Due once in September, still among the remaining months.
	if want := eurToCents(500); rowByName(view, "Desk").PlannedCents != want {
		t.Errorf("Desk PlannedCents = %d, want %d", rowByName(view, "Desk").PlannedCents, want)
	}
}

// TestBudgetForYearOtherYearsCountAllTwelveMonths confirms the "remaining
// months only" reduction is specific to the year "now" falls in — any other
// year (past or future relative to now) is entirely outside "now"'s year, so
// every month counts, same as before.
func TestBudgetForYearOtherYearsCountAllTwelveMonths(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	for _, year := range []int{2025, 2027} {
		view, err := b.ForYear(context.Background(), year, testNow, time.Time{})
		if err != nil {
			t.Fatalf("ForYear(%d): %v", year, err)
		}
		if want := eurToCents(12 * 1000); rowByName(view, "Rent").PlannedCents != want {
			t.Errorf("year %d: Rent PlannedCents = %d, want %d (full year)", year, rowByName(view, "Rent").PlannedCents, want)
		}
	}
}

func TestBudgetForYearDatedCategoryBeforeNowInCurrentYearContributesNothing(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": `{
		"groups": [
			{ "name": "OneOff", "kind": "private", "categories": [
				{ "id": "00000000-0000-4000-8000-000000000005", "name": "OldPurchase", "amount": 500, "date": "2026-03-01" }
			]}
		]
	}`})
	view, err := b.ForYear(context.Background(), 2026, testNow, time.Time{}) // testNow is July 2026
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if got := rowByName(view, "OldPurchase").PlannedCents; got != 0 {
		t.Errorf("OldPurchase PlannedCents = %d, want 0 (its month, March, is before now)", got)
	}
}

func TestBudgetForYearDatedCategoryOutsideYearContributesNothing(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	view, err := b.ForYear(context.Background(), 2027, testNow, time.Time{})
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if got := rowByName(view, "Desk").PlannedCents; got != 0 {
		t.Errorf("Desk PlannedCents in a year it's not due = %d, want 0", got)
	}
}

func rowByName(view BudgetView, name string) CategoryRow {
	for _, g := range view.Groups {
		for _, r := range g.Rows {
			if r.Name == name {
				return r
			}
		}
	}
	return CategoryRow{}
}

// TestCategoryRowForDated covers the three cases for a dated category with
// nothing due this period, compared against now at month granularity:
// future (shown, grayed out with its configured amount and month — see
// categoryRowFor), due this period (shown plainly, plannedCents > 0), past
// (hidden entirely).
func TestCategoryRowForDated(t *testing.T) {
	date := "2026-09-01"
	amount := 500.0
	desk := budgetdata.Category{Name: "Desk", Amount: amount, Date: &date}

	t.Run("future date shows grayed out with the amount and month", func(t *testing.T) {
		row, ok := categoryRowFor(desk, 0, false, testNow, false)
		if !ok {
			t.Fatal("expected a future dated category to still render")
		}
		if row.UpcomingMonth != "September 2026" {
			t.Errorf("UpcomingMonth = %q, want September 2026", row.UpcomingMonth)
		}
		if row.UpcomingCents != eurToCents(500) {
			t.Errorf("UpcomingCents = %d, want %d", row.UpcomingCents, eurToCents(500))
		}
		if row.PlannedCents != 0 {
			t.Errorf("PlannedCents = %d, want 0", row.PlannedCents)
		}
	})

	t.Run("due this period shows plainly", func(t *testing.T) {
		row, ok := categoryRowFor(desk, eurToCents(500), false, testNow, false)
		if !ok {
			t.Fatal("expected a due-this-period category to render")
		}
		if row.UpcomingMonth != "" {
			t.Errorf("UpcomingMonth = %q, want empty (not grayed out)", row.UpcomingMonth)
		}
		if row.PlannedCents != eurToCents(500) {
			t.Errorf("PlannedCents = %d, want %d", row.PlannedCents, eurToCents(500))
		}
	})

	t.Run("past date with nothing due this period is hidden entirely", func(t *testing.T) {
		pastDate := "2026-06-01"
		pastDesk := budgetdata.Category{Name: "Desk", Amount: amount, Date: &pastDate}
		if _, ok := categoryRowFor(pastDesk, 0, false, testNow, false); ok {
			t.Error("expected a past dated category to be hidden")
		}
	})

	t.Run("recurring categories are unaffected by date logic", func(t *testing.T) {
		rent := budgetdata.Category{Name: "Rent", Amount: 1000}
		row, ok := categoryRowFor(rent, eurToCents(1000), false, testNow, false)
		if !ok {
			t.Fatal("expected a recurring category to always render")
		}
		if row.UpcomingMonth != "" {
			t.Errorf("UpcomingMonth = %q, want empty for a recurring category", row.UpcomingMonth)
		}
	})

	t.Run("future date preview uses minimal_amount when minimal mode is on", func(t *testing.T) {
		minAmount := 300.0
		trip := budgetdata.Category{Name: "Trip", Amount: amount, MinimalAmount: &minAmount, Date: &date}
		row, ok := categoryRowFor(trip, 0, false, testNow, true)
		if !ok {
			t.Fatal("expected a future dated category to still render")
		}
		if row.UpcomingCents != eurToCents(300) {
			t.Errorf("UpcomingCents = %d, want %d (minimal_amount)", row.UpcomingCents, eurToCents(300))
		}
	})

	t.Run("future date preview stays at the full amount when minimal mode is off", func(t *testing.T) {
		minAmount := 300.0
		trip := budgetdata.Category{Name: "Trip", Amount: amount, MinimalAmount: &minAmount, Date: &date}
		row, ok := categoryRowFor(trip, 0, false, testNow, false)
		if !ok {
			t.Fatal("expected a future dated category to still render")
		}
		if row.UpcomingCents != eurToCents(500) {
			t.Errorf("UpcomingCents = %d, want %d (full amount)", row.UpcomingCents, eurToCents(500))
		}
	})
}

func TestCategoryRowForCarriesNoteAndURL(t *testing.T) {
	note := "PCB|Rigs Apex RGB BK"
	url := "https://pcbuild.bg/p-pcb-rigs-apex-rgb-bk-50462"
	c := budgetdata.Category{Name: "Computer", Amount: 3060, Note: &note, Url: &url}

	row, ok := categoryRowFor(c, eurToCents(3060), false, testNow, false)
	if !ok {
		t.Fatal("expected the category to render")
	}
	if row.Note != note {
		t.Errorf("Note = %q, want %q", row.Note, note)
	}
	if row.URL != url {
		t.Errorf("URL = %q, want %q", row.URL, url)
	}
}

func TestBudgetEvictForcesRefetch(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	ctx := context.Background()

	bf, err := b.File(ctx)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if len(bf.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(bf.Groups))
	}

	// Change the underlying file — still cached until evicted.
	b.FS.(fstest.MapFS)["budget.json"] = &fstest.MapFile{Data: []byte(`{"groups":[]}`)}
	bf, err = b.File(ctx)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if len(bf.Groups) != 2 {
		t.Fatal("expected the cached result before eviction")
	}

	b.Evict()
	bf, err = b.File(ctx)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if len(bf.Groups) != 0 {
		t.Fatal("expected a fresh fetch to see the now-changed file")
	}
}

// TestBudgetSplitsGroupsByKind confirms a company-kind group's categories
// land in CompanyGroups/CompanyTotalPlannedCents, not Groups/TotalPlannedCents,
// and vice versa for private.
func TestBudgetSplitsGroupsByKind(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithCompany})
	view, err := b.ForMonth(context.Background(), 2026, time.January, testNow)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if want := eurToCents(1000); view.TotalPlannedCents != want {
		t.Errorf("TotalPlannedCents (private) = %d, want %d", view.TotalPlannedCents, want)
	}
	if want := eurToCents(200); view.CompanyTotalPlannedCents != want {
		t.Errorf("CompanyTotalPlannedCents = %d, want %d", view.CompanyTotalPlannedCents, want)
	}
	if len(view.Groups) != 1 || view.Groups[0].Name != "Housing" {
		t.Errorf("Groups = %+v, want just Housing", view.Groups)
	}
	if len(view.CompanyGroups) != 1 || view.CompanyGroups[0].Name != "Office" {
		t.Errorf("CompanyGroups = %+v, want just Office", view.CompanyGroups)
	}
}

// TestBudgetGroupAllFutureStillShows confirms a group whose every category is
// a one-time cost planned for a later month still renders — PlannedCents sums
// to zero for the period, but it's not "empty": it has grayed-out planned
// rows worth showing as a reminder. Only a group with zero rows at all (see
// TestBudgetGroupAllPastIsHidden) should be dropped.
func TestBudgetGroupAllFutureStillShows(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": `{
		"groups": [
			{ "name": "Equipment", "kind": "company", "categories": [
				{ "id": "00000000-0000-4000-8000-000000000003", "name": "Desk", "amount": 3000, "date": "2026-09-01" },
				{ "id": "00000000-0000-4000-8000-000000000006", "name": "Monitor", "amount": 500, "date": "2026-09-01" }
			]}
		]
	}`})
	view, err := b.ForMonth(context.Background(), 2026, time.July, testNow) // testNow is July 2026, due date is September
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if len(view.CompanyGroups) != 1 {
		t.Fatalf("expected the Equipment group to still render, got %+v", view.CompanyGroups)
	}
	eq := view.CompanyGroups[0]
	if eq.PlannedCents != 0 {
		t.Errorf("PlannedCents = %d, want 0 (nothing due yet)", eq.PlannedCents)
	}
	if len(eq.Rows) != 2 {
		t.Fatalf("expected 2 planned rows, got %d", len(eq.Rows))
	}
	for _, r := range eq.Rows {
		if r.UpcomingMonth != "September 2026" {
			t.Errorf("%s: UpcomingMonth = %q, want September 2026", r.Name, r.UpcomingMonth)
		}
	}
}

// TestBudgetGroupAllPastIsHidden confirms a group that's genuinely empty for
// the period — every category a one-time cost whose month already passed,
// never logged — is dropped, not shown as a "-0" placeholder.
func TestBudgetGroupAllPastIsHidden(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": `{
		"groups": [
			{ "name": "Equipment", "kind": "company", "categories": [
				{ "id": "00000000-0000-4000-8000-000000000007", "name": "OldDesk", "amount": 3000, "date": "2026-01-01" }
			]}
		]
	}`})
	view, err := b.ForMonth(context.Background(), 2026, time.July, testNow) // testNow is July 2026, due date was January
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if len(view.CompanyGroups) != 0 {
		t.Errorf("expected the Equipment group to be hidden (nothing due, nothing planned), got %+v", view.CompanyGroups)
	}
}

// TestBudgetForMonthRowVisibilityFollowsViewedMonthNotRealNow confirms
// ForMonth grades a dated category's past/future status against the *viewed*
// month, not real current time — browsing month-by-month should show a
// one-time cost as upcoming, then active in its due month, then gone the
// month after, even while testNow (a fixed "today") never changes and stays
// well before all three viewed months.
func TestBudgetForMonthRowVisibilityFollowsViewedMonthNotRealNow(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": `{
		"groups": [
			{ "name": "Equipment", "kind": "company", "categories": [
				{ "id": "00000000-0000-4000-8000-000000000003", "name": "Desk", "amount": 3000, "date": "2026-10-01" }
			]}
		]
	}`})
	ctx := context.Background()

	// September: still upcoming -> grayed-out reminder.
	sep, err := b.ForMonth(ctx, 2026, time.September, testNow)
	if err != nil {
		t.Fatalf("ForMonth(September): %v", err)
	}
	if got := rowByName(BudgetView{Groups: sep.CompanyGroups}, "Desk"); got.UpcomingMonth != "October 2026" {
		t.Errorf("September: UpcomingMonth = %q, want October 2026", got.UpcomingMonth)
	}

	// October: due this month -> active, full amount.
	oct, err := b.ForMonth(ctx, 2026, time.October, testNow)
	if err != nil {
		t.Fatalf("ForMonth(October): %v", err)
	}
	if got := rowByName(BudgetView{Groups: oct.CompanyGroups}, "Desk").PlannedCents; got != eurToCents(3000) {
		t.Errorf("October: PlannedCents = %d, want %d", got, eurToCents(3000))
	}

	// November: already in the past relative to the viewed month -> the whole
	// group disappears, even though testNow (a fixed July "today") is well
	// before November too.
	nov, err := b.ForMonth(ctx, 2026, time.November, testNow)
	if err != nil {
		t.Fatalf("ForMonth(November): %v", err)
	}
	if len(nov.CompanyGroups) != 0 {
		t.Errorf("November: expected Equipment to be hidden (October has passed), got %+v", nov.CompanyGroups)
	}
}

// TestBudgetForYearCompanyAlwaysCountsAllTwelveMonths confirms company-kind
// categories count all twelve months of the viewed year even when it's the
// current year (unlike private, which only counts remaining months) — see
// ForYear's doc comment for why: company expenses feed the salary cascade,
// which projects a full year of company income, so the deduction has to
// span the same full year to stay consistent.
func TestBudgetForYearCompanyAlwaysCountsAllTwelveMonths(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithCompany})
	view, err := b.ForYear(context.Background(), 2026, testNow, time.Time{}) // testNow is July 2026
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if want := eurToCents(6 * 1000); view.TotalPlannedCents != want {
		t.Errorf("TotalPlannedCents (private, remaining months) = %d, want %d", view.TotalPlannedCents, want)
	}
	if want := eurToCents(12 * 200); view.CompanyTotalPlannedCents != want {
		t.Errorf("CompanyTotalPlannedCents (company, full year) = %d, want %d", view.CompanyTotalPlannedCents, want)
	}
}

func TestBudgetCompanyExpensesByMonth(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithCompany})
	byMonth, err := b.CompanyExpensesByMonth(context.Background(), 2026, time.Time{})
	if err != nil {
		t.Fatalf("CompanyExpensesByMonth: %v", err)
	}
	if len(byMonth) != 12 {
		t.Fatalf("expected all 12 months populated, got %d", len(byMonth))
	}
	for m, cents := range byMonth {
		if cents != eurToCents(200) {
			t.Errorf("month %v = %d, want %d", m, cents, eurToCents(200))
		}
	}
}

// TestComputeMonthWithBudget exercises the wiring in Tracker.compute that
// combines Budget's figures with Personal.NetIncomeCents.
func TestComputeMonthWithBudget(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})

	f := trk.ComputeMonth(context.Background(), 2026, time.March)
	if f.BudgetErr != "" {
		t.Fatalf("BudgetErr = %q", f.BudgetErr)
	}
	// Rent 1000 + Groceries 300 recur; Desk (due September) doesn't count in March.
	want := eurToCents(1000 + 300)
	if f.PrivateTotalPlannedCents != want {
		t.Errorf("PrivateTotalPlannedCents = %d, want %d", f.PrivateTotalPlannedCents, want)
	}
	// Balance now pairs March's expenses with January's funding income (the
	// viewed month shifted back two calendar months — see
	// fundingRangeForMonth), not March's own income (that's what
	// f.Personal, unaffected by this test, still reports — see
	// TestComputeMonthDeductsCompanyExpensesFromCompanyIncome). fullTracker's
	// fake Toggl backend only has data dated 2026-03-02, so January 2026 has
	// no tracked time, and January is far enough in the past (relative to
	// whenever this test actually runs) that there's no "expected remaining
	// work" projection for it either — so FundingPersonal.NetIncomeCents is
	// exactly 0 here.
	if f.FundingPersonal.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", f.FundingPersonal.Err)
	}
	if f.FundingPersonal.NetIncomeCents != 0 {
		t.Errorf("FundingPersonal.NetIncomeCents = %d, want 0 (January has no fake Toggl data)", f.FundingPersonal.NetIncomeCents)
	}
	if want := -f.PrivateTotalPlannedCents; !f.ShowBalance || f.BalanceCents != want {
		t.Errorf("ShowBalance/BalanceCents = %v/%d, want true/%d", f.ShowBalance, f.BalanceCents, want)
	}
	// Removed loans check since Loans field was removed from Figures struct
}

// TestComputeMonthDeductsCompanyExpensesFromCompanyIncome confirms company
// expenses reduce NetIncomeCents (via the salary cascade) rather than being
// subtracted a second time from PrivateTotalPlannedCents/BalanceCents.
func TestComputeMonthDeductsCompanyExpensesFromCompanyIncome(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithCompany})

	f := trk.ComputeMonth(context.Background(), 2026, time.March)
	if f.BudgetErr != "" || f.Personal.Err != "" {
		t.Fatalf("BudgetErr = %q, Personal.Err = %q", f.BudgetErr, f.Personal.Err)
	}
	if want := eurToCents(200); f.Personal.CompanyExpensesCents != want {
		t.Errorf("Personal.CompanyExpensesCents = %d, want %d", f.Personal.CompanyExpensesCents, want)
	}
	if want := eurToCents(1000); f.PrivateTotalPlannedCents != want {
		t.Errorf("PrivateTotalPlannedCents = %d, want %d (company expenses shouldn't appear here)", f.PrivateTotalPlannedCents, want)
	}
	if len(f.Personal.CompanyGroups) != 1 || f.Personal.CompanyGroups[0].Name != "Office" {
		t.Errorf("Personal.CompanyGroups = %+v, want just Office", f.Personal.CompanyGroups)
	}
	direct := trk.Personal.breakdown(float64(f.TotalCents)/100, 200, 1, trk.Personal.rulesFor(testMonth), SalaryDecision{Mode: SalaryFull}, companyStock{}, noDividend)
	if f.Personal.NetIncomeCents != direct.NetIncomeCents {
		t.Errorf("NetIncomeCents = %d, want %d (company income minus 200 company expenses, same cascade as breakdown(_, 200, 1))", f.Personal.NetIncomeCents, direct.NetIncomeCents)
	}
}

func TestComputeYearWithBudget(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})

	f := trk.ComputeYear(context.Background(), 2026)
	if f.FundingPersonal.Err != "" {
		t.Fatalf("FundingPersonal.Err = %q", f.FundingPersonal.Err)
	}
	// Balance pairs the year's expenses with the funding income range (the
	// viewed year's expense range shifted back two months — see
	// fundingRangeForYear), not f.Personal.NetIncomeCents (the year's own
	// income) — see TestComputeMonthWithBudget for why the two now differ.
	want := f.FundingPersonal.NetIncomeCents - f.PrivateTotalPlannedCents
	if !f.ShowBalance || f.BalanceCents != want {
		t.Errorf("ShowBalance/BalanceCents = %v/%d, want true/%d", f.ShowBalance, f.BalanceCents, want)
	}
}

// TestBudgetMinimalToggleFlipsGlobalState confirms ToggleMinimal/IsMinimal
// flip a single global flag, not tied to any particular month.
func TestBudgetMinimalToggleFlipsGlobalState(t *testing.T) {
	b := &Budget{}
	if b.IsMinimal() {
		t.Fatal("expected minimal mode to start off")
	}
	if on := b.ToggleMinimal(); !on {
		t.Error("first toggle should turn minimal mode on")
	}
	if !b.IsMinimal() {
		t.Error("IsMinimal should report on after toggling on")
	}
	if on := b.ToggleMinimal(); on {
		t.Error("second toggle should turn minimal mode back off")
	}
	if b.IsMinimal() {
		t.Error("IsMinimal should report off after toggling back off")
	}
}

// testBudgetJSONWithMinimal has a recurring category with a minimal_amount,
// one without (Rent — a fixed cost that should stay full even in minimal
// mode), and a one-off dated category with a minimal_amount, for testing
// ForMonth's minimal-mode substitution.
const testBudgetJSONWithMinimal = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 1000 },
      { "id": "00000000-0000-4000-8000-000000000008", "name": "Restaurants", "amount": 500, "minimal_amount": 200 }
    ]},
    { "name": "OneOff", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000009", "name": "Trip", "amount": 800, "minimal_amount": 300, "date": "2026-09-01" }
    ]}
  ]
}`

func TestBudgetForMonthFullAmountsWhenMinimalOff(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithMinimal})
	view, err := b.ForMonth(context.Background(), 2026, time.September, testNow)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if want := eurToCents(500); rowByName(view, "Restaurants").PlannedCents != want {
		t.Errorf("Restaurants PlannedCents = %d, want %d (minimal off)", rowByName(view, "Restaurants").PlannedCents, want)
	}
}

// TestBudgetForMonthSubstitutesMinimalAmountWhenOn confirms a category with
// a minimal_amount uses it once minimal mode is toggled on, a category
// without one (Rent) stays at its full amount, and a dated category due
// this month also respects minimal mode.
func TestBudgetForMonthSubstitutesMinimalAmountWhenOn(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithMinimal})
	b.ToggleMinimal()

	view, err := b.ForMonth(context.Background(), 2026, time.September, testNow)
	if err != nil {
		t.Fatalf("ForMonth: %v", err)
	}
	if want := eurToCents(200); rowByName(view, "Restaurants").PlannedCents != want {
		t.Errorf("Restaurants PlannedCents = %d, want %d (minimal)", rowByName(view, "Restaurants").PlannedCents, want)
	}
	if want := eurToCents(1000); rowByName(view, "Rent").PlannedCents != want {
		t.Errorf("Rent PlannedCents = %d, want %d (no minimal_amount configured, stays full)", rowByName(view, "Rent").PlannedCents, want)
	}
	if want := eurToCents(300); rowByName(view, "Trip").PlannedCents != want {
		t.Errorf("Trip (dated, due this month) PlannedCents = %d, want %d (minimal)", rowByName(view, "Trip").PlannedCents, want)
	}
}

// TestBudgetForYearIgnoresMinimalMode confirms year view always uses full
// amounts, even when the global minimal-mode flag is on.
func TestBudgetForYearIgnoresMinimalMode(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithMinimal})
	b.ToggleMinimal()

	view, err := b.ForYear(context.Background(), 2027, testNow, time.Time{}) // entirely future relative to testNow -> all 12 months count
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if want := eurToCents(12 * 500); rowByName(view, "Restaurants").PlannedCents != want {
		t.Errorf("Restaurants PlannedCents = %d, want %d (year view unaffected by minimal mode)", rowByName(view, "Restaurants").PlannedCents, want)
	}
}

// TestComputeMonthMinimalModeFields confirms Figures.MinimalMode/
// MinimalToggleURL are wired up from the global Budget flag for month view.
func TestComputeMonthMinimalModeFields(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})

	off := trk.ComputeMonth(context.Background(), 2026, time.March)
	if off.MinimalMode {
		t.Error("MinimalMode should start false")
	}
	if off.MinimalToggleURL != "/2026/3?minimal=toggle" {
		t.Errorf("MinimalToggleURL = %q, want /2026/3?minimal=toggle", off.MinimalToggleURL)
	}

	trk.Budget.ToggleMinimal()
	on := trk.ComputeMonth(context.Background(), 2026, time.March)
	if !on.MinimalMode {
		t.Error("MinimalMode should be true after toggling on")
	}
}

// TestComputeYearMinimalModeAlwaysFalse confirms year view never reports
// minimal mode as on, even when the global flag is toggled on, and doesn't
// expose a toggle URL (the toggle only lives in month view).
func TestComputeYearMinimalModeAlwaysFalse(t *testing.T) {
	trk := fullTracker()
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": testBudgetJSON})
	trk.Budget.ToggleMinimal()

	f := trk.ComputeYear(context.Background(), 2026)
	if f.MinimalMode {
		t.Error("MinimalMode should stay false in year view even when the global flag is on")
	}
	if f.MinimalToggleURL != "" {
		t.Errorf("MinimalToggleURL = %q, want empty in year view", f.MinimalToggleURL)
	}
}

// testBudgetJSONWithOverrides covers 0, 1, and 2+ zero-amount overrides in
// one fixture (the replacement for what excluded_months used to do): Rent
// has none (baseline), Flight has exactly one (August), Hotel has two
// (August and December) plus a minimal_amount, for testing that a zero
// override wins over minimal mode.
const testBudgetJSONWithOverrides = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 1000 }
    ]},
    { "name": "Trip", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000010", "name": "Flight", "amount": 400, "overrides": [{ "month": "2026-08-01", "amount": 0 }] },
      { "id": "00000000-0000-4000-8000-000000000011", "name": "Hotel", "amount": 200, "minimal_amount": 150, "overrides": [{ "month": "2026-08-01", "amount": 0 }, { "month": "2026-12-01", "amount": 0 }] }
    ]}
  ]
}`

// TestBudgetForMonthNoOverridesUnaffected covers the no-overrides case: a
// category without the field counts every month as before.
func TestBudgetForMonthNoOverridesUnaffected(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithOverrides})
	for _, m := range []time.Month{time.July, time.August, time.December} {
		view, err := b.ForMonth(context.Background(), 2026, m, testNow)
		if err != nil {
			t.Fatalf("ForMonth(%v): %v", m, err)
		}
		if want := eurToCents(1000); rowByName(view, "Rent").PlannedCents != want {
			t.Errorf("%v: Rent PlannedCents = %d, want %d (no overrides)", m, rowByName(view, "Rent").PlannedCents, want)
		}
	}
}

// TestBudgetForMonthSingleZeroOverrideZeroesOnlyThatMonth covers the
// single-override case (Flight, zeroed only in August). August itself now
// renders as a next-occurrence preview (see nextNonZeroMonth) rather than a
// bare 0 with no explanation — PlannedCents stays 0, but UpcomingMonth/
// UpcomingCents point at September, the next month Flight resumes; Overridden
// on that row describes September's own override status (none), not
// August's — same convention TestCategoryRowForDated's dated-future branch
// already uses for what it's previewing.
func TestBudgetForMonthSingleZeroOverrideZeroesOnlyThatMonth(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithOverrides})

	july, err := b.ForMonth(context.Background(), 2026, time.July, testNow)
	if err != nil {
		t.Fatalf("ForMonth(July): %v", err)
	}
	if want := eurToCents(400); rowByName(BudgetView{Groups: july.CompanyGroups}, "Flight").PlannedCents != want {
		t.Errorf("July: Flight PlannedCents = %d, want %d", rowByName(BudgetView{Groups: july.CompanyGroups}, "Flight").PlannedCents, want)
	}

	aug, err := b.ForMonth(context.Background(), 2026, time.August, testNow)
	if err != nil {
		t.Fatalf("ForMonth(August): %v", err)
	}
	augFlight := rowByName(BudgetView{Groups: aug.CompanyGroups}, "Flight")
	if augFlight.PlannedCents != 0 {
		t.Errorf("August (zero override): Flight PlannedCents = %d, want 0", augFlight.PlannedCents)
	}
	if want := eurToCents(400); augFlight.UpcomingCents != want {
		t.Errorf("August: Flight UpcomingCents = %d, want %d (September's normal amount)", augFlight.UpcomingCents, want)
	}
	if augFlight.UpcomingMonth != "September 2026" {
		t.Errorf("August: Flight UpcomingMonth = %q, want September 2026 (next non-zero month)", augFlight.UpcomingMonth)
	}
	if augFlight.Overridden {
		t.Error("August: Flight.Overridden should be false (describes September, which has no override)")
	}

	sep, err := b.ForMonth(context.Background(), 2026, time.September, testNow)
	if err != nil {
		t.Fatalf("ForMonth(September): %v", err)
	}
	sepFlight := rowByName(BudgetView{Groups: sep.CompanyGroups}, "Flight")
	if want := eurToCents(400); sepFlight.PlannedCents != want {
		t.Errorf("September: Flight PlannedCents = %d, want %d (override month is August only)", sepFlight.PlannedCents, want)
	}
	if sepFlight.Overridden {
		t.Error("September: Flight.Overridden should be false (no override this month)")
	}
}

// TestBudgetForMonthMultipleZeroOverridesZeroEach covers the
// multiple-overrides case (Hotel, zeroed in both August and December).
func TestBudgetForMonthMultipleZeroOverridesZeroEach(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithOverrides})

	for _, m := range []time.Month{time.August, time.December} {
		view, err := b.ForMonth(context.Background(), 2026, m, testNow)
		if err != nil {
			t.Fatalf("ForMonth(%v): %v", m, err)
		}
		if got := rowByName(BudgetView{Groups: view.CompanyGroups}, "Hotel").PlannedCents; got != 0 {
			t.Errorf("%v (zero override): Hotel PlannedCents = %d, want 0", m, got)
		}
	}
	for _, m := range []time.Month{time.July, time.November} {
		view, err := b.ForMonth(context.Background(), 2026, m, testNow)
		if err != nil {
			t.Fatalf("ForMonth(%v): %v", m, err)
		}
		if want := eurToCents(200); rowByName(BudgetView{Groups: view.CompanyGroups}, "Hotel").PlannedCents != want {
			t.Errorf("%v (no override): Hotel PlannedCents = %d, want %d", m, rowByName(BudgetView{Groups: view.CompanyGroups}, "Hotel").PlannedCents, want)
		}
	}
}

// TestBudgetForYearZeroOverridesReduceContribution confirms year view sums a
// category's overridden months using the override (0 here) rather than its
// full amount — company categories count all 12 months of the viewed year,
// so 2 zero-overridden months should mean 10 months' worth, not 12.
func TestBudgetForYearZeroOverridesReduceContribution(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithOverrides})
	view, err := b.ForYear(context.Background(), 2026, testNow, time.Time{})
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if want := eurToCents(10 * 200); rowByName(BudgetView{Groups: view.CompanyGroups}, "Hotel").PlannedCents != want {
		t.Errorf("Hotel PlannedCents = %d, want %d (12 months minus 2 zero-overridden)", rowByName(BudgetView{Groups: view.CompanyGroups}, "Hotel").PlannedCents, want)
	}
	if want := eurToCents(11 * 400); rowByName(BudgetView{Groups: view.CompanyGroups}, "Flight").PlannedCents != want {
		t.Errorf("Flight PlannedCents = %d, want %d (12 months minus 1 zero-overridden)", rowByName(BudgetView{Groups: view.CompanyGroups}, "Flight").PlannedCents, want)
	}
}

// TestBudgetCompanyExpensesByMonthRespectsOverrides confirms the per-month
// company-expense breakdown (which feeds the salary cascade for year view)
// is 0 for zero-overridden months.
func TestBudgetCompanyExpensesByMonthRespectsOverrides(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithOverrides})
	byMonth, err := b.CompanyExpensesByMonth(context.Background(), 2026, time.Time{})
	if err != nil {
		t.Fatalf("CompanyExpensesByMonth: %v", err)
	}
	if got := byMonth[time.August]; got != 0 {
		t.Errorf("August = %d, want 0 (both Flight and Hotel zero-overridden)", got)
	}
	if want := eurToCents(400); byMonth[time.December] != want {
		t.Errorf("December = %d, want %d (Flight counts, only Hotel zero-overridden)", byMonth[time.December], want)
	}
	if want := eurToCents(400 + 200); byMonth[time.July] != want {
		t.Errorf("July = %d, want %d (neither overridden)", byMonth[time.July], want)
	}
}

// TestBudgetZeroOverrideWinsOverMinimalAmount confirms a zero override is 0
// even when minimal mode is on and the category has a non-zero
// minimal_amount — an override always wins over minimal mode.
func TestBudgetZeroOverrideWinsOverMinimalAmount(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithOverrides})
	b.ToggleMinimal()

	aug, err := b.ForMonth(context.Background(), 2026, time.August, testNow)
	if err != nil {
		t.Fatalf("ForMonth(August): %v", err)
	}
	if got := rowByName(BudgetView{Groups: aug.CompanyGroups}, "Hotel").PlannedCents; got != 0 {
		t.Errorf("August (zero override, minimal on): Hotel PlannedCents = %d, want 0, not minimal_amount", got)
	}

	july, err := b.ForMonth(context.Background(), 2026, time.July, testNow)
	if err != nil {
		t.Fatalf("ForMonth(July): %v", err)
	}
	if want := eurToCents(150); rowByName(BudgetView{Groups: july.CompanyGroups}, "Hotel").PlannedCents != want {
		t.Errorf("July (no override, minimal on): Hotel PlannedCents = %d, want %d (minimal_amount applies normally)", rowByName(BudgetView{Groups: july.CompanyGroups}, "Hotel").PlannedCents, want)
	}
}

// TestCategoryRowForFutureDatedOverriddenMonthPreviewIsZero confirms a
// future-dated one-off category whose due month is itself zero-overridden
// shows a 0 planned preview, not its configured amount.
func TestCategoryRowForFutureDatedOverriddenMonthPreviewIsZero(t *testing.T) {
	date := "2026-09-01"
	desk := budgetdata.Category{Name: "Desk", Amount: 500, Date: &date, Overrides: []budgetdata.Override{{Month: "2026-09-15", Amount: 0}}}

	row, ok := categoryRowFor(desk, 0, false, testNow, false)
	if !ok {
		t.Fatal("expected a future dated category to still render")
	}
	if row.UpcomingMonth != "September 2026" {
		t.Errorf("UpcomingMonth = %q, want September 2026", row.UpcomingMonth)
	}
	if row.UpcomingCents != 0 {
		t.Errorf("UpcomingCents = %d, want 0 (due month is zero-overridden)", row.UpcomingCents)
	}
	if !row.Overridden {
		t.Error("Overridden should be true (due month has an override)")
	}
}

// testBudgetJSONWithNonZeroOverride covers a real-price override: Flight's
// normal amount is 400 with a minimal_amount of 100, but September's real,
// known price (427.42) overrides both.
const testBudgetJSONWithNonZeroOverride = `{
  "groups": [
    { "name": "Trip", "kind": "company", "categories": [
      { "id": "00000000-0000-4000-8000-000000000010", "name": "Flight", "amount": 400, "minimal_amount": 100, "overrides": [{ "month": "2026-09-01", "amount": 427.42 }] }
    ]}
  ]
}`

// TestBudgetForMonthNonZeroOverrideReplacesRecurringAmount confirms a
// non-zero override replaces the recurring amount for its month only, and
// flags the row as Overridden.
func TestBudgetForMonthNonZeroOverrideReplacesRecurringAmount(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithNonZeroOverride})

	sep, err := b.ForMonth(context.Background(), 2026, time.September, testNow)
	if err != nil {
		t.Fatalf("ForMonth(September): %v", err)
	}
	sepFlight := rowByName(BudgetView{Groups: sep.CompanyGroups}, "Flight")
	if want := eurToCents(427.42); sepFlight.PlannedCents != want {
		t.Errorf("September: Flight PlannedCents = %d, want %d (override)", sepFlight.PlannedCents, want)
	}
	if !sepFlight.Overridden {
		t.Error("September: Flight.Overridden should be true")
	}

	aug, err := b.ForMonth(context.Background(), 2026, time.August, testNow)
	if err != nil {
		t.Fatalf("ForMonth(August): %v", err)
	}
	augFlight := rowByName(BudgetView{Groups: aug.CompanyGroups}, "Flight")
	if want := eurToCents(400); augFlight.PlannedCents != want {
		t.Errorf("August: Flight PlannedCents = %d, want %d (no override, normal amount)", augFlight.PlannedCents, want)
	}
	if augFlight.Overridden {
		t.Error("August: Flight.Overridden should be false")
	}
}

// TestBudgetOverrideWinsOverMinimalMode confirms an override applies
// unconditionally over minimal-budget mode — the overridden month keeps its
// override amount, not minimal_amount, while other months still substitute
// minimal_amount normally.
func TestBudgetOverrideWinsOverMinimalMode(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithNonZeroOverride})
	b.ToggleMinimal()

	sep, err := b.ForMonth(context.Background(), 2026, time.September, testNow)
	if err != nil {
		t.Fatalf("ForMonth(September): %v", err)
	}
	if want := eurToCents(427.42); rowByName(BudgetView{Groups: sep.CompanyGroups}, "Flight").PlannedCents != want {
		t.Errorf("September (minimal on): Flight PlannedCents = %d, want %d (override wins)", rowByName(BudgetView{Groups: sep.CompanyGroups}, "Flight").PlannedCents, want)
	}

	aug, err := b.ForMonth(context.Background(), 2026, time.August, testNow)
	if err != nil {
		t.Fatalf("ForMonth(August): %v", err)
	}
	if want := eurToCents(100); rowByName(BudgetView{Groups: aug.CompanyGroups}, "Flight").PlannedCents != want {
		t.Errorf("August (minimal on, no override): Flight PlannedCents = %d, want %d (minimal_amount applies normally)", rowByName(BudgetView{Groups: aug.CompanyGroups}, "Flight").PlannedCents, want)
	}
}

// TestBudgetOverriddenNeverSetInYearView confirms the Overridden marker is
// always false in year view, even when one of the summed months has an
// override — a marker only makes sense for a single viewed month.
func TestBudgetOverriddenNeverSetInYearView(t *testing.T) {
	b := newTestBudget(t, map[string]string{"budget.json": testBudgetJSONWithNonZeroOverride})
	view, err := b.ForYear(context.Background(), 2026, testNow, time.Time{})
	if err != nil {
		t.Fatalf("ForYear: %v", err)
	}
	if rowByName(BudgetView{Groups: view.CompanyGroups}, "Flight").Overridden {
		t.Error("year view should never set Overridden, even though September has an override")
	}
}

// TestNextNonZeroMonthSingleOverride confirms the immediate next month is
// found when only the viewed month is zero-overridden.
func TestNextNonZeroMonthSingleOverride(t *testing.T) {
	date := "2026-08-01"
	c := budgetdata.Category{Name: "Trip", Amount: 100, Overrides: []budgetdata.Override{{Month: date, Amount: 0}}}
	ref := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)

	next, ok := nextNonZeroMonth(c, ref, false)
	if !ok {
		t.Fatal("expected a next non-zero month to be found")
	}
	if want := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

// TestNextNonZeroMonthConsecutiveOverridesSkipsAllOfThem confirms the scan
// walks past multiple consecutive zero-overridden months, not just the next
// one, and that it correctly crosses a calendar year boundary.
func TestNextNonZeroMonthConsecutiveOverridesSkipsAllOfThem(t *testing.T) {
	c := budgetdata.Category{Name: "Trip", Amount: 100, Overrides: []budgetdata.Override{
		{Month: "2026-11-01", Amount: 0},
		{Month: "2026-12-01", Amount: 0},
		{Month: "2027-01-01", Amount: 0},
	}}
	ref := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)

	next, ok := nextNonZeroMonth(c, ref, false)
	if !ok {
		t.Fatal("expected a next non-zero month to be found")
	}
	if want := time.Date(2027, time.February, 1, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v (first month past all three consecutive overrides)", next, want)
	}
}

// TestNextNonZeroMonthNonZeroOverrideCounts confirms a future month with a
// non-zero override (not just a reversion to the normal amount) counts as
// the next occurrence too.
func TestNextNonZeroMonthNonZeroOverrideCounts(t *testing.T) {
	c := budgetdata.Category{Name: "Trip", Amount: 100, Overrides: []budgetdata.Override{
		{Month: "2026-08-01", Amount: 0},
		{Month: "2026-09-01", Amount: 250}, // a real, known price for September
	}}
	ref := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)

	next, ok := nextNonZeroMonth(c, ref, false)
	if !ok {
		t.Fatal("expected a next non-zero month to be found")
	}
	if want := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

// TestNextNonZeroMonthUsesMinimalAmount confirms minimal-budget mode is
// honored when deciding whether a future month is "zero" — a category whose
// minimal_amount is itself 0 should be skipped in minimal mode even without
// an explicit override for that month.
func TestNextNonZeroMonthUsesMinimalAmount(t *testing.T) {
	zero := 0.0
	c := budgetdata.Category{Name: "Clothes", Amount: 100, MinimalAmount: &zero}
	ref := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)

	if _, ok := nextNonZeroMonth(c, ref, true); ok {
		t.Error("expected no next non-zero month in minimal mode when minimal_amount is 0")
	}
	next, ok := nextNonZeroMonth(c, ref, false)
	if !ok || !next.Equal(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("outside minimal mode: next = %v, ok = %v, want September 2026", next, ok)
	}
}

// TestCategoryRowForRecurringZeroOverrideShowsNextOccurrence is the direct
// regression test for the reported bug: a recurring category zeroed out for
// the viewed month via an override (e.g. "trip skipped this month") must
// show when it resumes and for how much, not a bare, unexplained 0.
func TestCategoryRowForRecurringZeroOverrideShowsNextOccurrence(t *testing.T) {
	ref := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	trip := budgetdata.Category{Name: "Trip", Amount: 250, Overrides: []budgetdata.Override{{Month: "2026-08-01", Amount: 0}}}

	row, ok := categoryRowFor(trip, 0, true, ref, false)
	if !ok {
		t.Fatal("expected the category to still render")
	}
	if row.PlannedCents != 0 {
		t.Errorf("PlannedCents = %d, want 0", row.PlannedCents)
	}
	if row.UpcomingMonth != "September 2026" {
		t.Errorf("UpcomingMonth = %q, want September 2026", row.UpcomingMonth)
	}
	if want := eurToCents(250); row.UpcomingCents != want {
		t.Errorf("UpcomingCents = %d, want %d", row.UpcomingCents, want)
	}
}

// TestCategoryRowForRecurringNonOverriddenZeroIsUnreachable documents why
// categoryRowFor's new branch is gated on overridden, not just plannedCents ==
// 0: a recurring category's normal amount is always > 0 (ValidateBudget), so
// overridden=false with plannedCents==0 shouldn't occur in practice for a
// recurring category — but if it somehow did (e.g. a future relaxation of
// that invariant), it must fall through to the plain zero row rather than
// scanning for a next occurrence that isn't actually meaningful.
func TestCategoryRowForRecurringNonOverriddenZeroIsUnreachable(t *testing.T) {
	ref := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	rent := budgetdata.Category{Name: "Rent", Amount: 900}

	row, ok := categoryRowFor(rent, 0, false, ref, false)
	if !ok {
		t.Fatal("expected the category to still render")
	}
	if row.UpcomingMonth != "" {
		t.Errorf("UpcomingMonth = %q, want empty (overridden=false must not trigger the preview branch)", row.UpcomingMonth)
	}
}

// TestEurToCentsRoundsDecimalAmounts confirms eurToCents rounds to the
// nearest cent, including cases affected by float64 imprecision.
func TestEurToCentsRoundsDecimalAmounts(t *testing.T) {
	tests := []struct {
		name  string
		euros float64
		want  int
	}{
		{"two decimals", 427.42, 42742},
		{"whole number", 432, 43200},
		{"float64 imprecision near .99", 19.99, 1999},
		{"single digit", 11, 1100},
		{"trailing zero decimal", 28.90, 2890},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eurToCents(tt.euros); got != tt.want {
				t.Errorf("eurToCents(%v) = %d, want %d", tt.euros, got, tt.want)
			}
		})
	}
}
