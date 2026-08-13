package tracker

import (
	"context"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

// bulgaria is a schedule shaped like the real thing: employment starts in July
// 2026 with a minimum wage, and the following January's package moves three
// figures at once and leaves the rest alone.
func bulgaria() PersonalParams {
	p := params()
	p.Legislation = append(p.Legislation,
		LegislationPeriod{From: yearMonth{2026, time.July}, MinimumWage: f64(1077)},
		LegislationPeriod{From: yearMonth{2027, time.January}, MinimumWage: f64(1150),
			Employer: cappedAt(0.199, 2400), Employee: cappedAt(0.1378, 2400)},
	)
	return p
}

// TestRulesCarryForward is the reason for nesting: an entry states what
// changed, not what stayed. Repeating unchanged figures in every entry is how
// one of them eventually gets repeated wrong.
func TestRulesCarryForward(t *testing.T) {
	p := bulgaria()

	base := params().rulesFor(testMonth)

	before := p.rulesFor(yearMonth{2026, time.June})
	if before.MinimumEUR != 0 {
		t.Errorf("June 2026 has a floor of %.2f — employment had not started", before.MinimumEUR)
	}
	// The schedules still apply, because this fixture's opening period is in
	// force from the start of time — not because anything reaches backwards.
	if !reflect.DeepEqual(before.Employer, base.Employer) {
		t.Errorf("June 2026 lost its employer schedule: %+v", before.Employer)
	}

	summer := p.rulesFor(yearMonth{2026, time.August})
	if summer.MinimumEUR != 1077 {
		t.Errorf("August 2026 minimum = %.2f, want 1077", summer.MinimumEUR)
	}
	// July's entry named only the minimum wage, so everything else is still
	// the baseline.
	if !reflect.DeepEqual(summer.Employer, base.Employer) {
		t.Errorf("July's entry changed a schedule it never mentioned: %+v", summer.Employer)
	}

	after := p.rulesFor(yearMonth{2027, time.March})
	if after.MinimumEUR != 1150 {
		t.Errorf("March 2027 minimum = %.2f, want the January package's 1150", after.MinimumEUR)
	}
	if !reflect.DeepEqual(after.Employer.Bands, Bands{{From: 0, Rate: 0.199}, {From: 2400, Rate: 0}}) {
		t.Errorf("March 2027 employer = %v, want the January package", after.Employer.Bands)
	}
	// And the figures January did not mention are carried forward, not reset.
	if !reflect.DeepEqual(after.IncomeTax, base.IncomeTax) {
		t.Errorf("January reset the tax bands it never mentioned: %v", after.IncomeTax)
	}
}

// TestARateChangeReachesTheArithmetic: the rates are not just labels. A period
// that raises the employer contribution has to change the salary it can fund.
func TestARateChangeReachesTheArithmetic(t *testing.T) {
	p := bulgaria()
	income := 5000.0

	before := p.breakdown(income, 0, 1, p.rulesFor(yearMonth{2026, time.December}), SalaryDecision{Mode: SalaryFull})
	after := p.breakdown(income, 0, 1, p.rulesFor(yearMonth{2027, time.January}), SalaryDecision{Mode: SalaryFull})

	if before.GrossSalaryCents <= after.GrossSalaryCents {
		t.Errorf("gross was %d before the rate rise and %d after — a higher employer rate must buy less salary",
			before.GrossSalaryCents, after.GrossSalaryCents)
	}
	if rateOf(before.EmployerRate) == rateOf(after.EmployerRate) {
		t.Errorf("the labelled rate stayed %s across a change that moved it", rateOf(before.EmployerRate))
	}
}

// TestInsurableCapIsDatedToo: the ceiling is legislation like everything else —
// now the boundary of the rate: 0 band — and raising it lifts the contribution
// base with it.
func TestInsurableCapIsDatedToo(t *testing.T) {
	p := bulgaria()
	income := 20000.0 // comfortably past either cap

	before := p.breakdown(income, 0, 1, p.rulesFor(yearMonth{2026, time.December}), SalaryDecision{Mode: SalaryFull})
	after := p.breakdown(income, 0, 1, p.rulesFor(yearMonth{2027, time.January}), SalaryDecision{Mode: SalaryFull})

	if after.EmployeeContribCents <= before.EmployeeContribCents {
		t.Errorf("employee contribution %d before the cap rise, %d after — a higher ceiling means a bigger base",
			before.EmployeeContribCents, after.EmployeeContribCents)
	}
}

// TestOneAlwaysInForcePeriodAppliesToEveryMonth: a single period dated from the
// start of time resolves identically in every month, however far apart. This is
// how a file that never changes its figures is written, and how every test
// predating dated legislation still means what it meant.
func TestOneAlwaysInForcePeriodAppliesToEveryMonth(t *testing.T) {
	plain := params()
	want := Rules{
		Employer:  PartyRules{Bands: Bands{{From: 0, Rate: 0.1892}, {From: 2112, Rate: 0}}},
		Employee:  PartyRules{Bands: Bands{{From: 0, Rate: 0.1378}, {From: 2112, Rate: 0}}},
		IncomeTax: Bands{{From: 0, Rate: 0.10}},
	}
	for _, ym := range []yearMonth{{2020, time.January}, {2026, time.August}, {2030, time.December}} {
		if got := plain.rulesFor(ym); !reflect.DeepEqual(got, want) {
			t.Errorf("%s resolved to %+v, want %+v in every month", ym, got, want)
		}
	}
}

// TestMinimumWageIsEnforcedWhateverTheCompanyEarned is the requirement that
// started this: an employed person is owed the statutory minimum, so it
// appears whether or not the month produced it.
func TestMinimumWageIsEnforcedWhateverTheCompanyEarned(t *testing.T) {
	p := bulgaria()
	lean := p.breakdown(0, 0, 1, p.rulesFor(yearMonth{2026, time.July}), SalaryDecision{Mode: SalaryFull})

	if lean.GrossSalaryCents != 107700 {
		t.Errorf("gross = %d, want the 1077 minimum even on no income", lean.GrossSalaryCents)
	}
	if !lean.MinimumEnforced {
		t.Error("the salary is the legal minimum and the view does not say so")
	}
	if lean.EmployerContribCents == 0 || lean.EmployeeContribCents == 0 || lean.IncomeTaxCents == 0 {
		t.Errorf("a paid salary owes contributions and tax: %+v", lean)
	}
}

func TestMinimumWageDoesNotCapAGoodMonth(t *testing.T) {
	p := bulgaria()
	rich := p.breakdown(10000, 0, 1, p.rulesFor(yearMonth{2026, time.July}), SalaryDecision{Mode: SalaryFull})
	unfloored := params().breakdown(10000, 0, 1, params().rulesFor(testMonth), SalaryDecision{Mode: SalaryFull})

	if rich.GrossSalaryCents != unfloored.GrossSalaryCents {
		t.Errorf("gross = %d with a floor, %d without — a floor must not change an affordable salary",
			rich.GrossSalaryCents, unfloored.GrossSalaryCents)
	}
	if rich.MinimumEnforced {
		t.Errorf("a month that cleared the floor reports it as binding: %+v", rich)
	}
}

func TestMinimumWageBeforeEmploymentChangesNothing(t *testing.T) {
	before := yearMonth{2026, time.May}
	with := bulgaria().breakdown(500, 0, 1, bulgaria().rulesFor(before), SalaryDecision{Mode: SalaryFull})
	without := params().breakdown(500, 0, 1, params().rulesFor(testMonth), SalaryDecision{Mode: SalaryFull})

	if !reflect.DeepEqual(with, without) {
		t.Errorf("May 2026 differs with legislation configured:\n with = %+v\n want = %+v", with, without)
	}
}

// TestLegislationAppliesPerMonthAcrossAYear: a year spanning a January package
// is summed against both sets of figures, not one of them twice.
func TestLegislationAppliesPerMonthAcrossAYear(t *testing.T) {
	p := bulgaria()

	year := p.breakdownMonths(make([]float64, 12), nil, yearMonth{2026, time.January})
	if want := 6 * 107700; year.GrossSalaryCents != want {
		t.Errorf("2026 gross = %d, want %d — six months of floor and six of nothing", year.GrossSalaryCents, want)
	}
	next := p.breakdownMonths(make([]float64, 12), nil, yearMonth{2027, time.January})
	if want := 12 * 115000; next.GrossSalaryCents != want {
		t.Errorf("2027 gross = %d, want %d", next.GrossSalaryCents, want)
	}
	// One schedule all year, so one unlabelled line.
	if got := rateOf(next.EmployerRate); got != "19.9% up to 2,400" {
		t.Errorf("2027 employer rate labelled %q, want the schedule in force", got)
	}
}

// TestFloorFollowsThePayrollMonthNotTheIncomeMonth: the Expenses panel's
// cascade is funded by income from two months earlier, but the salary is paid —
// and owed — in the month being viewed. Indexing by the funding month would
// make the minimum wage take effect two months after employment did, and
// nothing on the page would say why.
func TestFloorFollowsThePayrollMonthNotTheIncomeMonth(t *testing.T) {
	trk := accountsTracker(t, testAccountsJSON)
	trk.Personal.Legislation = Legislation{{From: yearMonth{2026, time.July}, MinimumWage: f64(1077)}}

	july := trk.ComputeMonth(context.Background(), 2026, time.July)
	if july.FundingPersonal.GrossSalaryCents < 107700 {
		t.Errorf("July gross = %d, want at least the 1077 floor — the salary is paid in July, whatever funded it",
			july.FundingPersonal.GrossSalaryCents)
	}
	if !july.FundingPersonal.MinimumEnforced {
		t.Error("July does not report the floor as binding")
	}
	if june := trk.ComputeMonth(context.Background(), 2026, time.June); june.FundingPersonal.MinimumEnforced {
		t.Error("June is before employment and has a floor applied")
	}
}

// TestAMonthThatCannotPayPaysNothing: with expenses above income and no floor
// in force there is no salary, rather than a negative one propagating through
// the contributions and tax below it.
func TestAMonthThatCannotPayPaysNothing(t *testing.T) {
	p := params()
	underwater := p.breakdown(500, 2000, 1, p.rulesFor(testMonth), SalaryDecision{Mode: SalaryFull})
	if underwater.GrossSalaryCents != 0 {
		t.Fatalf("gross = %d, want 0 on a month that cannot pay", underwater.GrossSalaryCents)
	}
	if underwater.EmployerContribCents != 0 || underwater.EmployeeContribCents != 0 || underwater.IncomeTaxCents != 0 {
		t.Errorf("a salary nobody was paid owes contributions and tax: %+v", underwater)
	}

	// A floor is not an affordability question: the minimum is owed whether the
	// month funded it or not, so it is paid and charged for.
	floored := p.rulesFor(testMonth)
	floored.MinimumEUR = 1077
	got := p.breakdown(500, 2000, 1, floored, SalaryDecision{Mode: SalaryFull})
	if !got.MinimumEnforced || got.GrossSalaryCents != 107700 {
		t.Errorf("with a floor the month cannot fund, gross = %d, enforced = %v; want the minimum paid anyway",
			got.GrossSalaryCents, got.MinimumEnforced)
	}
}

func TestParseLegislation(t *testing.T) {
	ok, err := ParseLegislation([]LegislationEntry{
		{From: "2027-01", MinimumWage: f64(1150)},
		{From: "2026-07-01", MinimumWage: f64(1077)}, // a full date, and out of order
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ok) != 2 || ok[0].From != (yearMonth{2026, time.July}) {
		t.Errorf("parsed = %+v, want July 2026 first", ok)
	}

	bad := map[string][]LegislationEntry{
		"not a month":      {{From: "July 2026", MinimumWage: f64(1077)}},
		"no date at all":   {{MinimumWage: f64(1077)}},
		"a negative rate":  {{From: "2026-07", Contributions: employerEntry(band(0, -0.1))}},
		"two for a month":  {{From: "2026-07", MinimumWage: f64(1077)}, {From: "2026-07-15", MinimumWage: f64(1100)}},
		"changing nothing": {{From: "2026-07"}},
	}
	for name, entries := range bad {
		if _, err := ParseLegislation(entries); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestEnforcedMinimumIsVisible(t *testing.T) {
	f := Figures{
		Currency: "€",
		FundingPersonal: PersonalView{
			GrossSalaryCents: 107700, MinimumWageCents: 107700, MinimumEnforced: true,
			EmployerRate: []RateLine{{Rate: "18.92%"}},
		},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	for _, want := range []string{"statutory minimum", "1,077/month"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page never says %q", want)
		}
	}
	// The salary being the legal minimum is worth saying; what it cost beyond
	// the period's own income is not, since dividends cover it.
	if strings.Contains(body, "more than this period earned") {
		t.Error("the shortfall note is back")
	}

	quiet := Figures{Currency: "€", FundingPersonal: PersonalView{GrossSalaryCents: 500000, EmployerRate: []RateLine{{Rate: "18.92%"}}}}
	rec = httptest.NewRecorder()
	RenderPage(rec, quiet)
	if strings.Contains(rec.Body.String(), "statutory minimum") {
		t.Error("a comfortable month mentions the minimum wage")
	}
}

// TestNoLegislationIsVisible: zero contributions and zero tax render as a
// perfectly ordinary payslip, so the one thing that distinguishes "nothing is
// owed" from "nothing is configured" has to be on the page.
func TestNoLegislationIsVisible(t *testing.T) {
	bare := Figures{Currency: "€", FundingPersonal: PersonalView{
		GrossSalaryCents: 500000, NoLegislation: true, EmployerRate: []RateLine{{Rate: "18.92%"}},
	}}
	rec := httptest.NewRecorder()
	RenderPage(rec, bare)
	if body := rec.Body.String(); !strings.Contains(body, "No legislation is in force") {
		t.Error("a month with nothing configured reads as an ordinary payslip owing nothing")
	}

	configured := Figures{Currency: "€", FundingPersonal: PersonalView{
		GrossSalaryCents: 500000, EmployerRate: []RateLine{{Rate: "18.92%"}},
	}}
	rec = httptest.NewRecorder()
	RenderPage(rec, configured)
	if strings.Contains(rec.Body.String(), "No legislation is in force") {
		t.Error("a month with figures in force claims there are none")
	}
}

// TestNothingAppliesBeforeTheEarliestEntry: legislation only ever reaches
// forwards. If the earliest entry is 2026-01, nothing was in force in 2024 —
// not the oldest figures on file, and certainly not a built-in schedule. A rate
// that is nowhere in config.json must never appear in the arithmetic.
func TestNothingAppliesBeforeTheEarliestEntry(t *testing.T) {
	p := PersonalParams{Legislation: Legislation{
		{From: yearMonth{2026, time.January}, Employer: cappedAt(0.1892, 2112),
			Employee: cappedAt(0.1378, 2112), IncomeTax: Bands{{From: 0, Rate: 0.10}}},
		{From: yearMonth{2026, time.July}, MinimumWage: f64(1077)},
	}}

	past := p.rulesFor(yearMonth{2024, time.March})
	if !reflect.DeepEqual(past, Rules{}) {
		t.Errorf("March 2024 = %+v, want nothing in force — it is before every entry", past)
	}
	if !past.nothingInForce() {
		t.Error("a month before every entry does not report itself as having nothing in force")
	}

	// A salary computed there is charged nothing, and says so rather than
	// looking like an ordinary payslip that happens to owe zero.
	v := p.breakdown(5000, 0, 1, past, SalaryDecision{Mode: SalaryFull})
	if v.EmployerContribCents != 0 || v.EmployeeContribCents != 0 || v.IncomeTaxCents != 0 {
		t.Errorf("a month before the earliest entry was charged something: %+v", v)
	}
	if !v.NoLegislation {
		t.Error("the view does not flag a month with no legislation in force")
	}

	// January is unaffected: the entry applies from its own month onward.
	if in := p.rulesFor(yearMonth{2026, time.January}); in.nothingInForce() {
		t.Errorf("January 2026 = %+v, want the figures its own entry states", in)
	}
}

// TestRateSurvivesTheMobileFold: at 600px and under the ledger is two columns
// and .mid is hidden, so a rate that lived only there would vanish on a phone
// — and a third filled cell would push the amount onto its own line. Both
// halves of the swap have to be in the markup for the CSS to choose between
// them, and neither is visible to a test that only reads the wide layout.
func TestRateSurvivesTheMobileFold(t *testing.T) {
	f := Figures{
		Currency: "€",
		FundingPersonal: PersonalView{
			GrossSalaryCents: 500000,
			EmployerRate:     []RateLine{{Rate: "18.92% up to 2,112", Span: "Jan–Apr"}, {Rate: "20% up to 3,000", Span: "May–Dec"}},
			EmployeeRate:     []RateLine{{Rate: "13.78%"}},
			IncomeTaxRate:    []RateLine{{Rate: "10%"}},
		},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	// The wide column carries the marker class the narrow rule keys off.
	if !strings.Contains(body, `<span class="mid rate">`) {
		t.Error("the rate cell is not marked, so the mobile rule cannot hide it")
	}
	// And the same content exists again, stacked under the amount. Counted in
	// the salary rows only: .stack-m is now the shared class for every folded
	// second figure, so a global count would include budgets and hours too.
	rows := regexp.MustCompile(`(?s)<span class="label">(?:Employer social|Employee social|Income tax)</span>.*?</div>`)
	stacked := 0
	for _, row := range rows.FindAllString(body, -1) {
		stacked += strings.Count(row, `class="stack-m"`)
	}
	if stacked != 4 {
		t.Errorf("got %d narrow rate spans, want 4 — two employer lines plus employee and tax", stacked)
	}
	// Each line keeps its span with it rather than leaving it to wrap away.
	for _, want := range []string{
		`<span class="stack-m">18.92% up to 2,112 Jan–Apr</span>`,
		`<span class="stack-m">20% up to 3,000 May–Dec</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	// The old blended percentage is gone from the labels.
	for _, gone := range []string{"Employer social (", "Employee social (", "Income tax ("} {
		if strings.Contains(body, gone) {
			t.Errorf("%s...) is back in the label", gone)
		}
	}
}
