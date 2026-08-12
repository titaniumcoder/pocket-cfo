package tracker

import (
	"strings"
	"testing"
	"time"
)

func TestParseSalaryPlan(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries []SalaryEntry
		wantErr string // substring; "" means valid
	}{
		{"a closed range", []SalaryEntry{{From: "2026-04", To: "2026-06", Mode: "minimum"}}, ""},
		{"open ended", []SalaryEntry{{From: "2026-04", Mode: "none"}}, ""},
		{"a full date is a month", []SalaryEntry{{From: "2026-04-01", To: "2026-06-30", Mode: "full"}}, ""},
		{"no entries at all", nil, ""},
		{"no mode", []SalaryEntry{{From: "2026-04"}}, "has no mode"},
		{"a mode nobody defined", []SalaryEntry{{From: "2026-04", Mode: "half"}}, "not full, minimum or none"},
		{"unparseable from", []SalaryEntry{{From: "April", Mode: "none"}}, "is not a month"},
		{"unparseable to", []SalaryEntry{{From: "2026-04", To: "later", Mode: "none"}}, "is not a month"},
		{"to before from", []SalaryEntry{{From: "2026-06", To: "2026-04", Mode: "none"}}, "precedes"},
		{
			"two entries for one month",
			[]SalaryEntry{{From: "2026-04", Mode: "none"}, {From: "2026-04", Mode: "minimum"}},
			"two things about",
		},
		{
			"ranges that overlap",
			[]SalaryEntry{{From: "2026-04", To: "2026-08", Mode: "minimum"}, {From: "2026-06", Mode: "none"}},
			"overlapping",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSalaryPlan(tt.entries)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("rejected a valid plan: %v", err)
			case tt.wantErr == "":
			case err == nil:
				t.Fatalf("accepted, want a complaint about %q", tt.wantErr)
			case !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("err = %v, want one about %q", err, tt.wantErr)
			}
		})
	}
}

// TestModeForCarriesForwardAndFallsBack: an entry with no `to` runs until the
// next one, an entry with one ends there, and anything uncovered pays a full
// salary — so adding a salary block never changes a month it does not name.
func TestModeForCarriesForwardAndFallsBack(t *testing.T) {
	plan, err := ParseSalaryPlan([]SalaryEntry{
		{From: "2026-04", To: "2026-06", Mode: "minimum"},
		{From: "2026-09", Mode: "none"},
		{From: "2027-02", Mode: "full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		ym   yearMonth
		want SalaryMode
	}{
		{yearMonth{2026, time.March}, SalaryFull},    // before anything
		{yearMonth{2026, time.April}, SalaryMinimum}, // first covered
		{yearMonth{2026, time.June}, SalaryMinimum},  // last covered, inclusive
		{yearMonth{2026, time.July}, SalaryFull},     // the gap after a closed range
		{yearMonth{2026, time.September}, SalaryNone},
		{yearMonth{2026, time.December}, SalaryNone}, // open-ended carries
		{yearMonth{2027, time.January}, SalaryNone},  // across a year boundary
		{yearMonth{2027, time.February}, SalaryFull}, // until the next entry
		{yearMonth{2030, time.June}, SalaryFull},
	} {
		if got := plan.modeFor(tt.ym); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.ym, got, tt.want)
		}
	}
}

// TestMinimumWithoutAMinimumWageIsRefused: the two blocks are independent, and
// together they can mean "pay the minimum" where the minimum is zero. The page
// would show that as a salary of nothing and never say why.
func TestMinimumWithoutAMinimumWageIsRefused(t *testing.T) {
	l, err := ParseLegislation([]LegislationEntry{{From: "2026-07", MinimumWage: f64(1077)}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-05", To: "2026-08", Mode: "minimum"}})
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateSalaryAgainstLegislation(plan, l)
	if err == nil {
		t.Fatal("accepted a minimum month with no minimum wage in force")
	}
	if !strings.Contains(err.Error(), "May 2026") || !strings.Contains(err.Error(), `"none"`) {
		t.Errorf("err = %v, want the month named and the alternative offered", err)
	}

	// Once the wage is in force the same shape is fine.
	ok, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-07", To: "2026-08", Mode: "minimum"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSalaryAgainstLegislation(ok, l); err != nil {
		t.Errorf("rejected a minimum month that has a minimum wage: %v", err)
	}
	// And nothing is checked for the other modes.
	none, _ := ParseSalaryPlan([]SalaryEntry{{From: "2026-05", Mode: "none"}})
	if err := ValidateSalaryAgainstLegislation(none, l); err != nil {
		t.Errorf("a none month was checked against the minimum wage: %v", err)
	}
}

// TestMinimumIsPaidEvenWhenMoreWasAffordable is the whole point of the mode:
// the company earned enough for a larger salary and deliberately did not pay
// it, so the difference stays in the company.
func TestMinimumIsPaidEvenWhenMoreWasAffordable(t *testing.T) {
	p := bulgariaBands()
	r := p.rulesFor(yearMonth{2026, time.July}) // minimum wage 620.20 in force

	full := p.breakdown(6000, 0, 1, r, SalaryFull)
	minimum := p.breakdown(6000, 0, 1, r, SalaryMinimum)

	if full.GrossSalaryCents <= 62020 {
		t.Fatalf("full gross = %d, which is not above the minimum — this test would prove nothing", full.GrossSalaryCents)
	}
	if minimum.GrossSalaryCents != 62020 {
		t.Errorf("minimum gross = %d, want exactly the 620.20 minimum", minimum.GrossSalaryCents)
	}
	// The floor did not bind — it was chosen — and the page says the two
	// differently.
	if minimum.MinimumEnforced {
		t.Error("a chosen minimum reports itself as the floor binding")
	}
	if minimum.Mode != SalaryMinimum {
		t.Errorf("Mode = %q", minimum.Mode)
	}
	// Contributions and tax are charged on what was actually paid.
	if minimum.EmployerContribCents != 11734 {
		t.Errorf("employer contribution = %d, want 11734 — 18.92%% of 620.20", minimum.EmployerContribCents)
	}
	// And the company kept the rest: the same income bought far less payroll.
	if minimum.EmployerContribCents+minimum.GrossSalaryCents >= full.EmployerContribCents+full.GrossSalaryCents {
		t.Error("the minimum month cost the company as much as the full one")
	}
}

// TestNoSalaryChargesNothing: nobody was on the payroll, so there is no base
// for a contribution and no salary to tax — and crucially the minimum-base
// rule must not invent one.
func TestNoSalaryChargesNothing(t *testing.T) {
	p := bulgariaBands()
	r := p.rulesFor(yearMonth{2026, time.July})
	r.Employer.MinBase = 933 // the case that could invent a payroll

	got := p.breakdown(6000, 0, 1, r, SalaryNone)

	if got.GrossSalaryCents != 0 {
		t.Errorf("gross = %d, want 0", got.GrossSalaryCents)
	}
	if got.EmployerContribCents != 0 || got.EmployeeContribCents != 0 || got.IncomeTaxCents != 0 {
		t.Errorf("a month with no payroll owed something: %+v", got)
	}
	if got.NetIncomeCents != 0 {
		t.Errorf("net = %d, want 0", got.NetIncomeCents)
	}
	// The floor does not apply to a month nobody was employed in.
	if got.MinimumEnforced {
		t.Error("the statutory minimum was enforced on a month with no salary")
	}
	// The money stays in the company rather than vanishing.
	if got.CompanyIncomeCents != 600000 {
		t.Errorf("company income = %d, want the full 6,000 — no salary does not mean no income", got.CompanyIncomeCents)
	}
	// Nothing to charge means nothing to describe.
	if len(got.EmployerRate) != 0 {
		t.Errorf("EmployerRate = %+v, want none", got.EmployerRate)
	}
}

// TestARangeCountsTheMonthsThatDiffer: a year where two months drew nothing
// has no single mode, so the page reports the counts instead of picking one.
func TestARangeCountsTheMonthsThatDiffer(t *testing.T) {
	p := bulgariaBands()
	plan, err := ParseSalaryPlan([]SalaryEntry{
		{From: "2026-03", To: "2026-04", Mode: "minimum"},
		{From: "2026-08", To: "2026-09", Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Salary = plan

	year := p.breakdownMonths(repeat(3000, 12), nil, yearMonth{2026, time.January})
	if year.MonthsAtMinimum != 2 {
		t.Errorf("MonthsAtMinimum = %d, want 2", year.MonthsAtMinimum)
	}
	if year.MonthsWithoutSalary != 2 {
		t.Errorf("MonthsWithoutSalary = %d, want 2", year.MonthsWithoutSalary)
	}
	if year.Mode != "" {
		t.Errorf("Mode = %q, want empty — the months disagreed", year.Mode)
	}

	// A year that agrees keeps its mode and counts every month.
	all, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-01", Mode: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	p.Salary = all
	quiet := p.breakdownMonths(repeat(3000, 12), nil, yearMonth{2026, time.January})
	if quiet.Mode != SalaryNone || quiet.MonthsWithoutSalary != 12 {
		t.Errorf("Mode = %q, without = %d, want none/12", quiet.Mode, quiet.MonthsWithoutSalary)
	}
	if quiet.GrossSalaryCents != 0 {
		t.Errorf("a year drawing no salary paid %d", quiet.GrossSalaryCents)
	}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}
