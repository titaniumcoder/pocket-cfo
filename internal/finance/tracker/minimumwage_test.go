package tracker

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func bulgaria() PersonalParams {
	p := params()
	// One figure from July 2026, when the company became active, and a rise
	// the following January — which is what a real schedule looks like.
	p.MinimumWage = []MinimumWagePeriod{
		{From: yearMonth{2026, time.July}, AmountEUR: 1077},
		{From: yearMonth{2027, time.January}, AmountEUR: 1150},
	}
	return p
}

// TestMinimumWageStartsWithEmployment: before the first period there is no
// floor, because there was no job. The earliest entry is the start date.
func TestMinimumWageStartsWithEmployment(t *testing.T) {
	p := bulgaria()
	tests := []struct {
		ym   yearMonth
		want float64
	}{
		{yearMonth{2026, time.May}, 0},
		{yearMonth{2026, time.June}, 0},
		{yearMonth{2026, time.July}, 1077},
		{yearMonth{2026, time.December}, 1077},
		{yearMonth{2027, time.January}, 1150},
		{yearMonth{2030, time.June}, 1150}, // the latest period stays in force
	}
	for _, tt := range tests {
		if got := p.minimumFor(tt.ym); got != tt.want {
			t.Errorf("minimumFor(%s) = %.2f, want %.2f", tt.ym, got, tt.want)
		}
	}
}

// TestMinimumWageIsEnforcedWhateverTheCompanyEarned is the requirement: an
// employed person is owed the statutory minimum, so it appears in the budget
// whether or not the month produced it.
func TestMinimumWageIsEnforcedWhateverTheCompanyEarned(t *testing.T) {
	p := bulgaria()

	// A month with nothing coming in at all.
	lean := p.breakdown(0, 0, 1, p.minimumFor(yearMonth{2026, time.July}))
	if lean.GrossSalaryCents != 107700 {
		t.Errorf("gross = %d, want the 1077 minimum even on no income", lean.GrossSalaryCents)
	}
	if !lean.MinimumEnforced {
		t.Error("the salary is the legal minimum and the view does not say so")
	}
	// Contributions and tax are computed on what is actually paid, not on
	// what the company could afford.
	if lean.EmployerContribCents == 0 || lean.EmployeeContribCents == 0 || lean.IncomeTaxCents == 0 {
		t.Errorf("a paid salary owes contributions and tax: %+v", lean)
	}
	if lean.NetIncomeCents <= 0 {
		t.Errorf("net = %d, want the minimum less deductions", lean.NetIncomeCents)
	}

	// And the company is short by the whole cost, which is the fact that must
	// not be hidden: the money is owed from somewhere else.
	wantShort := lean.GrossSalaryCents + lean.EmployerContribCents
	if lean.ShortfallCents != wantShort {
		t.Errorf("shortfall = %d, want %d — the salary and its employer cost", lean.ShortfallCents, wantShort)
	}
}

// TestMinimumWageDoesNotCapAGoodMonth: it is a floor, not a target.
func TestMinimumWageDoesNotCapAGoodMonth(t *testing.T) {
	p := bulgaria()
	rich := p.breakdown(10000, 0, 1, p.minimumFor(yearMonth{2026, time.July}))
	unfloored := params().breakdown(10000, 0, 1, 0)

	if rich.GrossSalaryCents != unfloored.GrossSalaryCents {
		t.Errorf("gross = %d with a floor, %d without — a floor must not change an affordable salary", rich.GrossSalaryCents, unfloored.GrossSalaryCents)
	}
	if rich.MinimumEnforced {
		t.Error("the floor is reported as binding on a month that cleared it")
	}
	if rich.ShortfallCents != 0 {
		t.Errorf("shortfall = %d on a profitable month, want 0", rich.ShortfallCents)
	}
}

// TestMinimumWageBeforeEmploymentChangesNothing: the months before the start
// date must be bit-identical to a build without this feature, or every historic
// figure moves.
func TestMinimumWageBeforeEmploymentChangesNothing(t *testing.T) {
	before := yearMonth{2026, time.May}
	with := bulgaria().breakdown(500, 0, 1, bulgaria().minimumFor(before))
	without := params().breakdown(500, 0, 1, 0)

	with.MinimumWageCents, without.MinimumWageCents = 0, 0
	if !reflect.DeepEqual(with, without) {
		t.Errorf("May 2026 differs with the floor configured:\n with = %+v\n want = %+v", with, without)
	}
}

// TestMinimumWageAppliesPerMonthAcrossAYear: a year spanning a January rise is
// summed against both figures, not one of them twice.
func TestMinimumWageAppliesPerMonthAcrossAYear(t *testing.T) {
	p := bulgaria()
	// Six lean months of 2026 from July: 1077 each. Nothing before July.
	year := p.breakdownMonths(make([]float64, 12), nil, yearMonth{2026, time.January})
	if want := 6 * 107700; year.GrossSalaryCents != want {
		t.Errorf("2026 gross = %d, want %d — six months of floor and six of nothing", year.GrossSalaryCents, want)
	}

	// All of 2027 at the higher figure.
	next := p.breakdownMonths(make([]float64, 12), nil, yearMonth{2027, time.January})
	if want := 12 * 115000; next.GrossSalaryCents != want {
		t.Errorf("2027 gross = %d, want %d", next.GrossSalaryCents, want)
	}
	if next.MinimumWageCents != 115000 {
		t.Errorf("reported floor = %d, want the one in force", next.MinimumWageCents)
	}
}

func TestParseMinimumWage(t *testing.T) {
	ok, err := ParseMinimumWage([]MinimumWageEntry{
		{From: "2027-01", Amount: 1150},
		{From: "2026-07-01", Amount: 1077}, // a full date, and out of order
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ok) != 2 || ok[0].From != (yearMonth{2026, time.July}) || ok[1].AmountEUR != 1150 {
		t.Errorf("parsed = %+v, want July 2026 first", ok)
	}

	bad := map[string][]MinimumWageEntry{
		"not a month": {{From: "July 2026", Amount: 1077}},
		"empty from":  {{From: "", Amount: 1077}},
		"negative":    {{From: "2026-07", Amount: -1}},
		"two for one": {{From: "2026-07", Amount: 1077}, {From: "2026-07-15", Amount: 1100}},
	}
	for name, entries := range bad {
		if _, err := ParseMinimumWage(entries); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestEnforcedMinimumIsVisible: the arithmetic is only useful if the page says
// it. A salary the company could not fund is the whole point of a floor, and a
// figure that appears with no explanation reads as a bug.
func TestEnforcedMinimumIsVisible(t *testing.T) {
	f := Figures{
		Currency: "€",
		FundingPersonal: PersonalView{
			GrossSalaryCents: 107700,
			MinimumWageCents: 107700,
			MinimumEnforced:  true,
			ShortfallCents:   128077,
			EmployerPct:      "18.92", EmployeePct: "13.78", IncomeTaxPct: "10",
		},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	if !strings.Contains(body, "statutory minimum") {
		t.Error("the salary row does not say it is the legal minimum")
	}
	if !strings.Contains(body, "1,077/month") {
		t.Error("the row does not name the figure being enforced")
	}
	if !strings.Contains(body, "more than this period earned") {
		t.Error("the shortfall is computed and never shown — a salary the company cannot fund is the finding")
	}

	// And a month that clears the floor says nothing at all about it.
	quiet := Figures{Currency: "€", FundingPersonal: PersonalView{GrossSalaryCents: 500000, EmployerPct: "18.92", EmployeePct: "13.78", IncomeTaxPct: "10"}}
	rec = httptest.NewRecorder()
	RenderPage(rec, quiet)
	if strings.Contains(rec.Body.String(), "statutory minimum") {
		t.Error("a comfortable month mentions the minimum wage")
	}
}

// TestShortfallOnlyReportedWhenTheFloorCausedIt: without a floor the salary is
// solved to fit the income exactly, so any gap is the older "company expenses
// exceeded company income" case. That is a different fact, the zero salary
// already says it, and the shortfall's wording would describe it wrongly.
func TestShortfallOnlyReportedWhenTheFloorCausedIt(t *testing.T) {
	p := params() // no minimum wage configured

	// Expenses well past income: the salary is nothing and the company is
	// underwater, but not because of any floor.
	underwater := p.breakdown(500, 2000, 1, 0)
	if underwater.GrossSalaryCents != 0 {
		t.Fatalf("gross = %d, want 0 on a month that cannot pay", underwater.GrossSalaryCents)
	}
	if underwater.ShortfallCents != 0 {
		t.Errorf("shortfall = %d with no floor configured — that note is about the floor", underwater.ShortfallCents)
	}

	// The same month with a floor: now there is a salary, and a real shortfall.
	floored := p.breakdown(500, 2000, 1, 1077)
	if floored.ShortfallCents <= 0 {
		t.Errorf("shortfall = %d, want the cost the company has to find", floored.ShortfallCents)
	}
}

// TestFloorFollowsThePayrollMonthNotTheIncomeMonth: the Expenses panel's
// cascade is funded by income from two months earlier, but the salary is paid —
// and owed — in the month being viewed. Indexing the floor by the funding month
// would make the minimum wage take effect two months after employment does, and
// nothing on the page would say why.
func TestFloorFollowsThePayrollMonthNotTheIncomeMonth(t *testing.T) {
	trk := accountsTracker(t, testAccountsJSON)
	trk.Personal.MinimumWage = []MinimumWagePeriod{{From: yearMonth{2026, time.July}, AmountEUR: 1077}}

	// July 2026 is the first payroll month. Its funding month is May, before
	// employment — so a funding-indexed floor would find nothing here.
	july := trk.ComputeMonth(context.Background(), 2026, time.July)
	if july.FundingPersonal.GrossSalaryCents < 107700 {
		t.Errorf("July gross = %d, want at least the 1077 floor — the salary is paid in July, whatever funded it",
			july.FundingPersonal.GrossSalaryCents)
	}
	if !july.FundingPersonal.MinimumEnforced {
		t.Error("July does not report the floor as binding")
	}

	// June 2026 is before employment and must stay untouched.
	june := trk.ComputeMonth(context.Background(), 2026, time.June)
	if june.FundingPersonal.MinimumEnforced {
		t.Error("June is before employment and has a floor applied")
	}
}
