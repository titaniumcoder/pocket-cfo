package tracker

import (
	"context"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestActualStatus(t *testing.T) {
	tests := []struct {
		name       string
		row        CategoryRow
		complete   bool
		viewed     time.Month
		charged    map[string][]time.Month
		wantStatus string
	}{
		{
			name: "no actual, nothing to say",
			row:  CategoryRow{CategoryID: "a", PlannedCents: 40000},
		},
		{
			name:       "over plan fires immediately, regardless of coverage",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 40000, HasActual: true},
			wantStatus: ActualOver,
		},
		{
			// 5% of 350 is 17.50, so the €20 floor is what applies.
			name:       "sixteen euros over a 350 budget is on plan",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 36600, HasActual: true},
			wantStatus: "",
		},
		{
			// The case that prompted the band: 5% of 1500 is 75.
			name:       "two euros over a 1500 budget is on plan",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 150000, ActualCents: 150200, HasActual: true},
			complete:   true,
			wantStatus: "",
		},
		{
			// The case that separates the two halves of the rule: above the
			// €20 floor, below 5% of 1 500. Only the percentage can excuse it.
			name:       "fifty euros over a 1500 budget is still within five percent",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 150000, ActualCents: 155000, HasActual: true},
			wantStatus: "",
		},
		{
			name:       "eighty euros over a 1500 budget clears the five percent",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 150000, ActualCents: 158000, HasActual: true},
			wantStatus: ActualOver,
		},
		{
			name:       "twenty-one euros over a small budget clears the floor",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 4000, ActualCents: 6100, HasActual: true},
			wantStatus: ActualOver,
		},
		{
			name:       "under by less than the tolerance is on plan, not good news",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 150000, ActualCents: 149000, HasActual: true},
			complete:   true,
			wantStatus: "",
		},
		{
			name:       "under plan is withheld until the month is fully read",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 20000, HasActual: true},
			wantStatus: "",
		},
		{
			name:       "under plan once coverage is complete",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 20000, HasActual: true},
			complete:   true,
			wantStatus: ActualUnder,
		},
		{
			name:       "exactly on plan is not under",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 35000, HasActual: true},
			complete:   true,
			wantStatus: "",
		},
		{
			name:       "charged against a category planned at zero",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 0, ActualCents: 4000, HasActual: true},
			complete:   true,
			wantStatus: ActualUnbudgeted,
		},
		{
			// The tolerance excuses a figure for missing a number someone
			// chose. There is no number here, so nothing to excuse: an
			// unplanned charge is worth seeing at any size, because seeing it
			// is how it gets budgeted next time.
			name:       "even a small charge against a zero budget is worth seeing",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 0, ActualCents: 1500, HasActual: true},
			complete:   true,
			wantStatus: ActualUnbudgeted,
		},
		{
			name:       "a one-off charged in the wrong month outranks over",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 0, ActualCents: 180000, HasActual: true},
			viewed:     time.August,
			charged:    map[string][]time.Month{"laptop": {time.August}},
			wantStatus: ActualMistimed,
		},
		{
			name:       "a one-off due now but already charged elsewhere",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 180000},
			viewed:     time.October,
			charged:    map[string][]time.Month{"laptop": {time.August}},
			wantStatus: ActualMistimed,
		},
		{
			name:       "a one-off charged in its own month is fine",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 180000, ActualCents: 180000, HasActual: true},
			viewed:     time.October,
			charged:    map[string][]time.Month{"laptop": {time.October}},
			wantStatus: "",
		},
		{
			name:       "a recurring category can never be mistimed",
			row:        CategoryRow{CategoryID: "rent", PlannedCents: 90000, ActualCents: 90000, HasActual: true},
			viewed:     time.August,
			charged:    map[string][]time.Month{"rent": {time.July, time.August}},
			wantStatus: "",
		},
		{
			name:       "year view passes no charged map, so nothing is mistimed",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 0, ActualCents: 180000, HasActual: true},
			viewed:     time.August,
			wantStatus: ActualUnbudgeted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := actualStatus(tt.row, tt.complete, tt.viewed, tt.charged)
			if got != tt.wantStatus {
				t.Errorf("actualStatus = %q, want %q", got, tt.wantStatus)
			}
		})
	}
}

func TestApplyActualsFillsWithoutTouchingThePlan(t *testing.T) {
	bv := BudgetView{
		Groups: []CategoryGroupView{{
			Name:         "Food",
			PlannedCents: 55000,
			Rows: []CategoryRow{
				{Name: "Groceries", CategoryID: "food.groceries", PlannedCents: 35000},
				{Name: "Restaurants", CategoryID: "food.restaurants", PlannedCents: 20000},
			},
		}},
		TotalPlannedCents: 55000,
	}
	av := ActualsView{Present: true, Complete: true, ByCategory: map[string]int{"food.groceries": 36600}, TotalCents: 36600}

	ApplyActuals(&bv, av, time.August, nil)

	if bv.TotalPlannedCents != 55000 || bv.Groups[0].PlannedCents != 55000 {
		t.Error("ApplyActuals changed a planned total — actuals must be display-only")
	}
	if bv.Groups[0].Rows[0].PlannedCents != 35000 || bv.Groups[0].Rows[1].PlannedCents != 20000 {
		t.Error("ApplyActuals changed a row's planned figure")
	}
	if got := bv.Groups[0].Rows[0].ActualCents; got != 36600 {
		t.Errorf("Groceries ActualCents = %d, want 36600", got)
	}
	if bv.Groups[0].Rows[1].HasActual {
		t.Error("Restaurants has no recorded spending and must not be marked as having any")
	}
	if got := bv.Groups[0].ActualCents; got != 36600 {
		t.Errorf("group ActualCents = %d, want the sum of its rows", got)
	}
}

func TestApplyActualsDoesNothingWithoutAFile(t *testing.T) {
	bv := BudgetView{Groups: []CategoryGroupView{{Rows: []CategoryRow{{CategoryID: "a", PlannedCents: 100}}}}}
	ApplyActuals(&bv, ActualsView{}, time.August, nil)
	if bv.Groups[0].Rows[0].HasActual || bv.Groups[0].Rows[0].ActualStatus != "" {
		t.Error("a period with no imported file must be left completely untouched")
	}
}

func TestUnmatchedCentsSplitsByKind(t *testing.T) {
	bv := BudgetView{
		Groups:        []CategoryGroupView{{Rows: []CategoryRow{{CategoryID: "food.groceries", HasActual: true}}}},
		CompanyGroups: []CategoryGroupView{{Rows: []CategoryRow{{CategoryID: "co.office", HasActual: true}}}},
	}
	av := ActualsView{Present: true, ByCategory: map[string]int{
		"food.groceries": 100, // matched
		"co.office":      200, // matched
		"co.gone":        300, // company-kind, no row
		"gone.entirely":  400, // not in budget.json at all
	}}
	private, company := UnmatchedCents(bv, av, map[string]bool{"co.office": true, "co.gone": true})
	if company != 300 {
		t.Errorf("company unmatched = %d, want 300", company)
	}
	if private != 400 {
		t.Errorf("private unmatched = %d, want 400 (an unknown id has no kind and falls to private)", private)
	}
}

// actualsTracker builds a tracker over the shared test budget plus an
// optional actuals file.
func actualsTracker(t *testing.T, actuals map[string]string) *Tracker {
	t.Helper()
	trk := accountsTracker(t, testAccountsJSON)
	if actuals != nil {
		trk.Actuals = newTestActuals(t, actuals)
	}
	return trk
}

// augustActuals is a month of imported statements totalling 400, against the
// 1 000 the shared test budget plans. from/to decide whether the month counts
// as covered, which is the whole question when closing a month on it.
func augustActuals(from, to string) map[string]string {
	return map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"` + from + `","to":"` + to + `","imported_at":"2026-09-01"}],
			"transactions":[{"id":"x1","date":"2026-08-03","description":"LIDL","amount":400,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	}
}

// TestAClosedMonthClosesOnItsActuals is the point of letting actuals reach the
// roll-forward. The balance opens at 2 000 on the 31 July read; August plans
// 1 000 of private spending but the bank says 400 was spent.
//
//	without actuals   Aug closes 1 000, Sept closes 0,   Oct opens at 0
//	with actuals      Aug closes 1 600, Sept closes 600, Oct opens at 600
//
// The 600 is exactly the gap between what August planned and what it spent.
// September has no imported file and still closes on its plan, so one month
// switching to actuals must not drag the rest with it.
func TestAClosedMonthClosesOnItsActuals(t *testing.T) {
	ctx := context.Background()

	without := actualsTracker(t, nil).ComputeMonth(ctx, 2026, time.October)
	with := actualsTracker(t, augustActuals("2026-08-01", "2026-08-31")).ComputeMonth(ctx, 2026, time.October)

	if without.OpeningBalanceCents != 0 {
		t.Fatalf("October opening without actuals = %d, want 0 — the plan spends 1 000 a month out of 2 000", without.OpeningBalanceCents)
	}
	if with.OpeningBalanceCents != 60000 {
		t.Errorf("October opening with actuals = %d, want 60000 — August should have closed on the 400 the bank saw, not the 1 000 it planned", with.OpeningBalanceCents)
	}
}

// TestAPartlyImportedMonthStillClosesOnItsPlan is the guard on that. Coverage
// running to the 15th means the rest of August was never read, so closing on
// those transactions would treat a half-imported month as a frugal one and
// carry the invented surplus forward for good, with nothing on the page
// saying so. A month is closed on its statements only when they cover it.
func TestAPartlyImportedMonthStillClosesOnItsPlan(t *testing.T) {
	ctx := context.Background()

	partial := actualsTracker(t, augustActuals("2026-08-01", "2026-08-15")).ComputeMonth(ctx, 2026, time.October)
	if partial.OpeningBalanceCents != 0 {
		t.Errorf("October opening = %d, want 0 — half a month of statements must not close August", partial.OpeningBalanceCents)
	}

	// And the same file, covering the whole month, does close it — otherwise
	// this test would pass on any bug that ignored actuals entirely.
	full := actualsTracker(t, augustActuals("2026-08-01", "2026-08-31")).ComputeMonth(ctx, 2026, time.October)
	if full.OpeningBalanceCents == partial.OpeningBalanceCents {
		t.Error("complete and partial coverage closed August identically, so coverage is not being consulted at all")
	}
}

// TestTheCompanySideClosesOnItsActualsToo: the company total does not simply
// subtract, it goes into the payroll cascade as what the company had to spend
// before paying anyone. Swapping planned for actual there has to move the
// closing balance, or the company pot would keep rolling on the plan while
// the private one had already switched to the bank.
func TestTheCompanySideClosesOnItsActualsToo(t *testing.T) {
	ctx := context.Background()
	const officeID = "00000000-0000-4000-8000-000000000002"

	build := func(actuals map[string]string) *Tracker {
		trk := accountsTracker(t, `{"accounts":[
			{"name":"Private","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]},
			{"name":"Company","kind":"company","balances":[{"as_of":"2026-07-31","balance":5000}]}
		]}`)
		trk.Budget = newTestBudget(t, map[string]string{"budget.json": `{"groups":[
			{"name":"Housing","kind":"private","categories":[{"id":"00000000-0000-4000-8000-000000000001","name":"Rent","amount":1000}]},
			{"name":"Office","kind":"company","categories":[{"id":"` + officeID + `","name":"Desk","amount":800}]}
		]}`})
		trk.Personal = bulgariaBands()
		plan, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-01", Mode: "minimum"}})
		if err != nil {
			t.Fatal(err)
		}
		trk.Personal.Salary = plan
		if actuals != nil {
			trk.Actuals = newTestActuals(t, actuals)
		}
		return trk
	}

	without := build(nil).ComputeMonth(ctx, 2026, time.October)
	with := build(map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"c1","date":"2026-08-04","description":"DESK","amount":100,"account":"A","category":"` + officeID + `"}]}`,
	}).ComputeMonth(ctx, 2026, time.October)

	if !without.FundingPersonal.ShowCompanyBalance || !with.FundingPersonal.ShowCompanyBalance {
		t.Fatal("no company balance to compare — the rest of this test would prove nothing")
	}
	gap := with.FundingPersonal.CompanyOpeningCents - without.FundingPersonal.CompanyOpeningCents
	if gap != 70000 {
		t.Errorf("company opening moved by %d, want 70000 — August planned 800 of company spend and the bank saw 100", gap)
	}
}

// monthTracker builds a tracker whose balance opens the month `offset` months
// from now, with the first few days of that month imported. Everything is
// derived from the real calendar because "is this the current month" is
// answered against it, not against the month under test — so the only way to
// test a past month is to ask for one relative to today.
func monthTracker(t *testing.T, offset, privateSpendEUR int, withCompany bool) (*Tracker, yearMonth) {
	t.Helper()
	now := time.Now()
	viewed := yearMonth{now.Year(), now.Month()}.addMonths(offset)

	// The last day of the month before the one viewed: a reading closes its
	// month, so the 28th is only a legal date in February.
	asOf := time.Date(viewed.Year, viewed.Month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Format("2006-01-02")
	key := fmt.Sprintf("%04d-%02d", viewed.Year, int(viewed.Month))

	accounts := fmt.Sprintf(`{"accounts":[{"name":"P","kind":"private","balances":[{"as_of":%q,"balance":2000}]}`, asOf)
	if withCompany {
		accounts += fmt.Sprintf(`,{"name":"C","kind":"company","balances":[{"as_of":%q,"balance":5000}]}`, asOf)
	}
	trk := accountsTracker(t, accounts+`]}`)

	if withCompany {
		trk.Budget = newTestBudget(t, map[string]string{"budget.json": `{"groups":[
			{"name":"Housing","kind":"private","categories":[{"id":"00000000-0000-4000-8000-000000000001","name":"Rent","amount":1000}]},
			{"name":"Office","kind":"company","categories":[{"id":"00000000-0000-4000-8000-000000000002","name":"Desk","amount":800}]}
		]}`})
	}
	trk.Actuals = newTestActuals(t, map[string]string{
		"actuals/" + key + ".json": fmt.Sprintf(
			`{"month":%q,"coverage":[{"account":"A","from":"%s-01","to":"%s-05","imported_at":"%s-05"}],
			"transactions":[{"id":"x1","date":"%s-03","description":"LIDL","amount":%d,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
			key, key, key, key, key, privateSpendEUR),
	})
	return trk, viewed
}

// TestTheCurrentMonthShowsSpentAndProjectedTogether: mid-month the plan has
// already charged the whole month while the statements have only reached the
// day they were imported, so the two figures answer different questions —
// "where will this end up" and "where does it stand today". Neither replaces
// the other, and the gap between them is exactly the spending the plan has
// booked and the bank has not seen yet.
func TestTheCurrentMonthShowsSpentAndProjectedTogether(t *testing.T) {
	trk, viewed := monthTracker(t, 0, 400, false)
	f := trk.ComputeMonth(context.Background(), viewed.Year, viewed.Month)

	if !f.ShowActualBalance {
		t.Fatal("the current month showed only one balance")
	}
	if want := f.ActualAvailableCents - f.PrivateActualCents; f.ActualBalanceCents != want {
		t.Errorf("ActualBalanceCents = %d, want %d (what there was to spend, less what the bank has seen spent)", f.ActualBalanceCents, want)
	}
	// Nothing crossed this month and no dividend is declared, so what there was
	// to spend is the same figure in both columns and the whole gap is spending.
	if f.ActualAvailableCents != f.AvailableCents {
		t.Fatalf("the fixture has money crossing it (%d), so the gap below is not spending alone", f.ArrivedPrivatelyCents)
	}
	if want := f.PrivateTotalPlannedCents - f.PrivateActualCents; f.ActualBalanceCents-f.BalanceCents != want {
		t.Errorf("the two balances differ by %d, want %d — the gap is the planned spending not yet on a statement",
			f.ActualBalanceCents-f.BalanceCents, want)
	}
	if f.BalanceCents != f.OpeningBalanceCents+f.FundingPersonal.NetIncomeCents-f.PrivateTotalPlannedCents {
		t.Error("the projected balance stopped being the plan")
	}
}

// TestAClosedMonthAlsoShowsWhatTheBankSaw: a month that has been imported can
// say exactly what the account held, and a closed month is the case where it
// is not even an estimate — the statements are the whole story, and that
// figure is what the next month opens with. It used to show the plan alone,
// so a month that overspent its budget still displayed the balance it had
// been meant to end on.
func TestAClosedMonthAlsoShowsWhatTheBankSaw(t *testing.T) {
	trk, past := monthTracker(t, -3, 400, false)
	f := trk.ComputeMonth(context.Background(), past.Year, past.Month)

	if !f.ShowActualBalance {
		t.Fatal("a closed month with statements showed the plan alone")
	}
	if want := f.ActualAvailableCents - f.PrivateActualCents; f.ActualBalanceCents != want {
		t.Errorf("ActualBalanceCents = %d, want %d (what there was to spend, less what the bank saw spent)", f.ActualBalanceCents, want)
	}
	if f.BalanceCents != f.OpeningBalanceCents+f.FundingPersonal.NetIncomeCents-f.PrivateTotalPlannedCents {
		t.Error("the planned balance stopped being the plan")
	}
	if f.ActualBalanceCents == f.BalanceCents {
		t.Error("the two figures are identical, so this month proves nothing about showing both")
	}
}

// TestNothingImportedShowsOneFigure: the second figure is read off the
// statements, so with no statements there is nothing to read and the plan
// stands alone rather than being restated as fact. A year view is the same
// case for a different reason — it has no opening balance to add to.
func TestNothingImportedShowsOneFigure(t *testing.T) {
	ctx := context.Background()

	trk, viewed := monthTracker(t, 0, 400, false)
	trk.Actuals = nil
	if f := trk.ComputeMonth(ctx, viewed.Year, viewed.Month); f.ShowActualBalance {
		t.Error("a month with no statements at all showed a second figure")
	}

	trk, viewed = monthTracker(t, -3, 400, false)
	if f := trk.ComputeYear(ctx, viewed.Year); f.ShowActualBalance {
		t.Error("a year view showed a balance the bank saw; it has no opening balance to build one from")
	}
}

// TestTheLiveCompanyClosingIsTheRowsAboveIt is the company sibling, and it
// carries the same obligation the projected one does: a reader adds up the
// rows on the page, so the figure at the bottom has to be exactly their sum.
// Only the expense row differs between the two — the salary decision is a
// plan either way and is not re-derived from a half-imported month.
func TestTheLiveCompanyClosingIsTheRowsAboveIt(t *testing.T) {
	trk, viewed := monthTracker(t, 0, 400, true)
	trk.Personal = bulgariaBands()
	plan, err := ParseSalaryPlan([]SalaryEntry{{From: "2020-01", Mode: "minimum"}})
	if err != nil {
		t.Fatal(err)
	}
	trk.Personal.Salary = plan

	f := trk.ComputeMonth(context.Background(), viewed.Year, viewed.Month)
	if !f.ShowActualBalance || !f.FundingPersonal.ShowCompanyBalance {
		t.Fatal("no live company closing to check")
	}
	pv := f.FundingPersonal
	want := pv.CompanyOpeningCents + pv.CompanyIncomeCents - f.CompanyActualCents -
		pv.EmployerContribCents - pv.GrossSalaryCents
	if f.ActualCompanyClosingCents != want {
		t.Errorf("live closing = %d, want %d — the rows on the page do not add up to it", f.ActualCompanyClosingCents, want)
	}
	if f.ActualCompanyClosingCents == pv.CompanyClosingCents {
		t.Error("the live and projected company closings are identical, so the actual expenses never reached it")
	}
}

// TestTheLiveCompanyClosingLosesTheSameFiguresAsThePlannedOne: the live closing
// is re-derived by hand rather than sharing closeCompanyOver, so the two
// expressions can drift apart silently — and did, for the whole life of the
// dividend, because no fixture with a distribution ever reached this one. The
// distribution itself must not be in either: it hands the owner a claim, not
// money, and only the two taxes leave the bank.
func TestTheLiveCompanyClosingLosesTheSameFiguresAsThePlannedOne(t *testing.T) {
	f := Figures{
		ShowActuals:        true,
		CompanyActualCents: 40000,
		FundingPersonal: PersonalView{
			ShowCompanyBalance:    true,
			CompanyExpensesCents:  30000,
			CompanyOpeningCents:   5000000,
			CompanyIncomeCents:    400000,
			EmployerContribCents:  20000,
			GrossSalaryCents:      107700,
			DividendCents:         1000000,
			DividendTaxCents:      50000,
			CompanyProfitTaxCents: 100000,
		},
	}
	// Nothing has actually been paid yet: the taxes are declared, not remitted.
	f.publishBalanceTheBankSaw(1)

	pv := f.FundingPersonal
	want := pv.CompanyOpeningCents + pv.CompanyIncomeCents - f.CompanyActualCents -
		pv.EmployerContribCents - pv.GrossSalaryCents
	if f.ActualCompanyClosingCents != want {
		t.Errorf("live closing = %d, want %d — the Actual column charges what the statements say left, not what the plan declared", f.ActualCompanyClosingCents, want)
	}
	if f.ActualCompanyClosingCents == want-pv.DividendCents {
		t.Error("the gross distribution is being taken out of the bank again")
	}

	// Once the statements say the taxes were paid, the two columns agree —
	// which is the drift signature to watch: a term added to one expression and
	// not the other makes them disagree on a month where reality matched the
	// plan.
	f.CompanyActualCents = pv.CompanyExpensesCents
	f.CompanyCashOutCents = pv.DividendTaxCents + pv.CompanyProfitTaxCents
	f.publishBalanceTheBankSaw(1)
	planned := pv.plannedCompanyMonth(pv.CompanyOpeningCents).closesAt()
	if f.ActualCompanyClosingCents != planned {
		t.Errorf("a month that went exactly to plan closes at %d live and %d planned — the two expressions have drifted",
			f.ActualCompanyClosingCents, planned)
	}
}

// TestBothFiguresReachThePage: the pair is only useful side by side, in the
// column grammar the ledger already uses for planned against actual.
func TestBothFiguresReachThePage(t *testing.T) {
	trk, viewed := monthTracker(t, 0, 400, false)
	f := trk.ComputeMonth(context.Background(), viewed.Year, viewed.Month)
	if !f.ShowActualBalance {
		t.Fatal("nothing to render")
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	row := regexp.MustCompile(`<div class="row net balance[^"]*"><span class="label">Balance</span>(.*?)</div>`).FindStringSubmatch(body)
	if row == nil {
		t.Fatalf("no Balance row on the page:\n%s", body)
	}
	for _, want := range []string{formatEuro(f.BalanceCents), formatEuro(f.ActualBalanceCents)} {
		if !strings.Contains(row[1], want) {
			t.Errorf("the Balance row %q is missing %q", row[1], want)
		}
	}

	// Available to spend carries the same pair, for the same reason: a draw the
	// plan knows nothing about only shows up in one of the two columns.
	avail := regexp.MustCompile(`<div class="row net gap-above[^"]*"><span class="label">Available to spend</span>(.*?)</div>`).FindStringSubmatch(body)
	if avail == nil {
		t.Fatalf("no Available to spend row on the page:\n%s", body)
	}
	for _, want := range []string{formatEuro(f.AvailableCents), formatEuro(f.ActualAvailableCents)} {
		if !strings.Contains(avail[1], want) {
			t.Errorf("the Available to spend row %q is missing %q", avail[1], want)
		}
	}
}

// TestActualsChangeNoPlannedFigure: actuals move the roll-forward and nothing
// else, so every planned-based figure of the month on screen must still be
// bit-identical with and without them. August is the month the test snapshot
// opens, so nothing is carried into it and its Balance is pure plan — which
// is what makes it the right month to pin this on.
// TestAClosedMonthClosesOnItsActuals covers the other half.
func TestActualsChangeNoPlannedFigure(t *testing.T) {
	ctx := context.Background()
	month := time.August

	without := actualsTracker(t, nil).ComputeMonth(ctx, 2026, month)
	with := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"x1","date":"2026-08-03","description":"LIDL","amount":1000,"account":"A","category":"rent"}]}`,
	}).ComputeMonth(ctx, 2026, month)

	if !with.ShowActuals {
		t.Fatal("the actuals layer did not switch on — the rest of this test would prove nothing")
	}
	if with.BalanceCents != without.BalanceCents {
		t.Errorf("BalanceCents = %d with actuals, %d without", with.BalanceCents, without.BalanceCents)
	}
	if with.AvailableCents != without.AvailableCents {
		t.Errorf("AvailableCents = %d with actuals, %d without", with.AvailableCents, without.AvailableCents)
	}
	if with.OpeningBalanceCents != without.OpeningBalanceCents {
		t.Errorf("OpeningBalanceCents = %d with actuals, %d without", with.OpeningBalanceCents, without.OpeningBalanceCents)
	}
	if with.PrivateTotalPlannedCents != without.PrivateTotalPlannedCents {
		t.Errorf("PrivateTotalPlannedCents = %d with actuals, %d without", with.PrivateTotalPlannedCents, without.PrivateTotalPlannedCents)
	}
	if len(with.PrivateGroups) != len(without.PrivateGroups) {
		t.Fatalf("group count changed: %d vs %d", len(with.PrivateGroups), len(without.PrivateGroups))
	}
	for i := range with.PrivateGroups {
		if with.PrivateGroups[i].PlannedCents != without.PrivateGroups[i].PlannedCents {
			t.Errorf("group %q planned = %d with actuals, %d without",
				with.PrivateGroups[i].Name, with.PrivateGroups[i].PlannedCents, without.PrivateGroups[i].PlannedCents)
		}
		for j := range with.PrivateGroups[i].Rows {
			a, b := with.PrivateGroups[i].Rows[j], without.PrivateGroups[i].Rows[j]
			if a.PlannedCents != b.PlannedCents {
				t.Errorf("row %q planned = %d with actuals, %d without", a.Name, a.PlannedCents, b.PlannedCents)
			}
		}
	}
}

// TestNoActualsRendersByteIdentically: a month with no imported file must
// produce exactly the HTML it produced before this layer existed.
func TestNoActualsRendersByteIdentically(t *testing.T) {
	ctx := context.Background()

	unconfigured := actualsTracker(t, nil).ComputeMonth(ctx, 2026, time.August)
	empty := actualsTracker(t, map[string]string{}).ComputeMonth(ctx, 2026, time.August)

	var a, b strings.Builder
	recA, recB := httptest.NewRecorder(), httptest.NewRecorder()
	RenderPage(recA, unconfigured)
	RenderPage(recB, empty)
	a.Write(recA.Body.Bytes())
	b.Write(recB.Body.Bytes())

	if a.String() != b.String() {
		t.Error("a month with no actuals file rendered differently from one with the layer switched off entirely")
	}
	for _, marker := range []string{"with-actuals", "colhead", "Actually spent", "mistimed-note"} {
		if strings.Contains(a.String(), marker) {
			t.Errorf("the no-actuals page contains %q — nothing may be shown when there is nothing to show", marker)
		}
	}
}

func TestActualsErrorDegradesTheSectionNotThePage(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[],"transactions":[`, // malformed
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.ActualsErr == "" {
		t.Error("a broken actuals file must set ActualsErr")
	}
	if f.ShowActuals {
		t.Error("a broken actuals file must leave the layer off")
	}
	if f.BalanceCents == 0 && f.PrivateTotalPlannedCents == 0 {
		t.Error("the rest of the page should still compute")
	}
}

func TestActualsYearViewOnlyForPastYears(t *testing.T) {
	files := map[string]string{}
	for _, m := range []string{"01", "02"} {
		files["actuals/2026-"+m+".json"] = `{"month":"2026-` + m + `","coverage":[{"account":"A","from":"2026-` + m + `-01","to":"2026-` + m + `-28","imported_at":"2026-03-01"}],
			"transactions":[{"id":"y` + m + `","date":"2026-` + m + `-05","description":"X","amount":100,"account":"A","category":"rent"}]}`
	}
	trk := actualsTracker(t, files)

	// testNow is July 2026, so 2026 is the current year: ForYear projects
	// private spend forward from now, which actuals can't be compared with.
	current := trk.ComputeYear(context.Background(), 2026)
	if current.ShowActuals {
		t.Error("the current year must not show actuals — its planned figures are a forward projection")
	}
}

// TestDescriptionsNeverReachTheDashboard: computeActuals reads only
// ByCategory and TotalCents, so a description isn't in the struct the
// dashboard renders. That, not the 403, is what makes a leak impossible.
func TestDescriptionsNeverReachTheDashboard(t *testing.T) {
	const secret = "VERY-PRIVATE-MERCHANT-NAME"
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[
				{"id":"s1","date":"2026-08-03","description":"` + secret + `","amount":1000,"account":"A","category":"rent"},
				{"id":"s2","date":"2026-08-04","description":"` + secret + `-IGNORED","amount":-50,"account":"A","ignored":"refund of something"}
			]}`,
	})

	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if !f.ShowActuals {
		t.Fatal("the actuals layer did not switch on — this test would prove nothing")
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("a transaction description reached the dashboard HTML")
	}

	// And it is reachable through the admin path, so the check above isn't
	// passing merely because the data never loaded.
	sv := trk.ComputeSpending(context.Background(), 2026, time.August)
	if !sv.Present {
		t.Fatal("ComputeSpending found nothing — the fixture never loaded")
	}
	recS := httptest.NewRecorder()
	RenderSpending(recS, sv)
	if !strings.Contains(recS.Body.String(), secret) {
		t.Error("the admin drill-down should show descriptions; it showed none")
	}
	if !strings.Contains(recS.Body.String(), "refund of something") {
		t.Error("an ignored line must appear with its reason, so the page reconciles to the statement")
	}
}

// TestGroupHeaderColumnsMatchItsRows pins the alignment that shipped wrong:
// the header used to be a flex row with actual on the left and planned on the
// right — the reverse of both the Planned/Actual column header and the rows
// beneath it, and lining up with neither.
func TestGroupHeaderColumnsMatchItsRows(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"x1","date":"2026-08-03","description":"LIDL","amount":1200,"account":"A","category":"rent"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if !f.ShowActuals {
		t.Fatal("the actuals layer did not switch on")
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	header := regexp.MustCompile(`(?s)<div class="group-header"[^>]*>(.*?)</div>`).FindStringSubmatch(body)
	if header == nil {
		t.Fatal("no group header rendered")
	}
	cells := regexp.MustCompile(`<span class="(mid|amt)[^"]*">`).FindAllStringSubmatch(header[1], -1)
	if len(cells) != 2 || cells[0][1] != "mid" || cells[1][1] != "amt" {
		t.Fatalf("group header cells = %v, want mid then amt so they align with the rows", cells)
	}

	// The header's planned figure must be in .mid and rendered like every
	// row's, minus included — both are money leaving.
	midCell := regexp.MustCompile(`(?s)<span class="mid[^"]*">(.*?)</span>`)
	mid := midCell.FindStringSubmatch(header[1])
	if mid == nil || !strings.Contains(mid[1], "&minus;") {
		t.Errorf("header .mid = %q, want the planned figure as an expense", mid)
	}
	rows := regexp.MustCompile(`(?s)<div class="group-rows">(.*?)</div>\s*</div>`).FindStringSubmatch(body)
	if rows == nil {
		t.Fatal("no group rows rendered")
	}
	rowMid := midCell.FindStringSubmatch(rows[1])
	if rowMid == nil || !strings.Contains(rowMid[1], "&minus;") {
		t.Errorf("row .mid = %q, want the planned figure as an expense too", rowMid)
	}

	// And the column header exists on both ledgers, so the two numbers are labelled.
	if got := strings.Count(body, `class="row colhead"`); got != 2 {
		t.Errorf("found %d column headers, want one per ledger", got)
	}
}

// TestGroupCarriesTheWorstRowInside: a collapsed group has to say that
// something in it wants a look. Grading the group's own total would not —
// one category 300 over and another 300 under net out to a group that reads
// as perfectly on plan.
func TestGroupCarriesTheWorstRowInside(t *testing.T) {
	tests := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"nothing to say", []string{"", ""}, ""},
		{"one under", []string{"", ActualUnder}, ActualUnder},
		{"over outranks under", []string{ActualUnder, ActualOver}, ActualOver},
		{"order does not matter", []string{ActualOver, ActualUnder}, ActualOver},
		{"unbudgeted outranks under", []string{ActualUnder, ActualUnbudgeted}, ActualUnbudgeted},
		{"mistimed outranks everything", []string{ActualOver, ActualMistimed, ActualUnder}, ActualMistimed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ""
			for _, s := range tt.statuses {
				got = worseStatus(got, s)
			}
			if got != tt.want {
				t.Errorf("group status = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGroupFlagSurvivesANettingOut is the case the group total cannot show:
// the two rows cancel exactly, so the group's own numbers say on plan while
// one category inside is 300 over.
func TestGroupFlagSurvivesANettingOut(t *testing.T) {
	bv := BudgetView{Groups: []CategoryGroupView{{
		Name:         "Living",
		PlannedCents: 200000, // summed by buildBudgetView, not by ApplyActuals
		Rows: []CategoryRow{
			{CategoryID: "a", PlannedCents: 100000},
			{CategoryID: "b", PlannedCents: 100000},
		},
	}}}
	av := ActualsView{
		Present: true, Complete: true,
		ByCategory: map[string]int{"a": 130000, "b": 70000},
	}
	ApplyActuals(&bv, av, time.August, nil)

	g := bv.Groups[0]
	if g.ActualCents != g.PlannedCents {
		t.Fatalf("the fixture does not net out: actual %d, planned %d", g.ActualCents, g.PlannedCents)
	}
	if g.Status != ActualOver {
		t.Errorf("group status = %q, want %q — one row is 300 over", g.Status, ActualOver)
	}
}
