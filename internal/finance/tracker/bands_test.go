package tracker

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The vectors in this file come from payroll documents produced by a Bulgarian
// accountant and a real payslip, plus published UK NI thresholds — figures that
// are authoritative rather than derived from this package's own arithmetic.
// They are the record of what bands are for; there is no design doc beside them.

func band(from, rate float64) BandEntry { return BandEntry{From: from, Rate: &rate} }

func employerEntry(bs ...BandEntry) *ContributionsEntry {
	return &ContributionsEntry{Employer: &PartyEntry{Bands: bs}}
}

// schedule is one party's rules; a minBase of 0 means none.
func schedule(minBase float64, bs ...Band) *PartySchedule {
	s := &PartySchedule{Bands: bs}
	if minBase > 0 {
		s.MinBase = &minBase
	}
	return s
}

// bulgariaBands is the July 2026 package: a minimum wage, a minimum insurable
// base that happens to equal it, one rate per party up to the ceiling and
// nothing above it, and a flat 10% tax.
func bulgariaBands() PersonalParams {
	return PersonalParams{Legislation: Legislation{{
		From:        yearMonth{2026, time.July},
		MinimumWage: f64(620.20),
		Employer:    schedule(620.20, Band{From: 0, Rate: 0.1892}, Band{From: 2111.64, Rate: 0}),
		Employee:    schedule(620.20, Band{From: 0, Rate: 0.1378}, Band{From: 2111.64, Rate: 0}),
		IncomeTax:   Bands{{From: 0, Rate: 0.10}},
	}}}
}

// atGross drives the cascade to an exact gross by making that figure the
// statutory floor on a period with no income. The floor is applied after the
// affordability arithmetic, so everything below it — both contributions, the
// tax base, the tax, the net — is computed on precisely the salary named.
func atGross(p PersonalParams, r Rules, gross float64) PersonalView {
	r.MinimumEUR = gross
	return p.breakdown(0, 0, 1, r, SalaryFull)
}

func checkCents(t *testing.T, v PersonalView, want map[string]int) {
	t.Helper()
	got := map[string]int{
		"employer": v.EmployerContribCents,
		"employee": v.EmployeeContribCents,
		"gross":    v.GrossSalaryCents,
		"tax":      v.IncomeTaxCents,
		"net":      v.NetIncomeCents,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %d, want %d", k, got[k], w)
		}
	}
}

// TestVectorAboveTheFloorBelowTheCeiling is the ordinary Bulgarian month, and
// the one that pins per-line cent rounding at the tax base: 936.00 less a
// contribution of 128.98 is a tax base of 807.02, not of 807.0192.
func TestVectorAboveTheFloorBelowTheCeiling(t *testing.T) {
	p := bulgariaBands()
	v := atGross(p, p.rulesFor(yearMonth{2026, time.July}), 936.00)

	checkCents(t, v, map[string]int{
		"gross":    93600,
		"employee": 12898, // 13.78% of 936.00
		"tax":      8070,  // 10% of 807.02
		"net":      72632,
	})
}

// TestVectorAtTheMinimumWage is the vector that decides how this cascade
// rounds. Full float precision through to the end nets 481.1076 and displays
// 481.11; rounding each line to the cent as a payslip does — 85.44, then
// 534.56, then 53.46 — nets exactly the 481.10 the accountant filed.
//
// Note it charges on 620.00 rather than on the 620.20 minimum insurable base,
// so it runs against a schedule with no minBase. The same salary with the МОД
// in force owes 85.46 and 117.34 instead, which is exactly
// TestVectorTheMinimumWageBinding below — between them the two vectors pin what
// minBase does and does not do.
func TestVectorAtTheMinimumWage(t *testing.T) {
	p := PersonalParams{Legislation: Legislation{{
		From:      fromTheStart,
		Employer:  schedule(0, Band{From: 0, Rate: 0.1892}, Band{From: 2111.64, Rate: 0}),
		Employee:  schedule(0, Band{From: 0, Rate: 0.1378}, Band{From: 2111.64, Rate: 0}),
		IncomeTax: Bands{{From: 0, Rate: 0.10}},
	}}}
	v := atGross(p, p.rulesFor(testMonth), 620.00)

	checkCents(t, v, map[string]int{
		"gross":    62000,
		"employee": 8544,  // 13.78% of 620.00
		"employer": 11730, // 18.92% of 620.00
		"tax":      5346,  // 10% of 534.56
		"net":      48110,
	})
	if cost := v.GrossSalaryCents + v.EmployerContribCents; cost != 73730 {
		t.Errorf("total employer cost = %d, want 73730", cost)
	}
}

// TestVectorAtTheCeilingAcceptsOneCent records a decision rather than a fact.
//
// The accountant computes each fund separately and rounds each to the cent
// before summing — 8.38% + 2.20% + 3.20% for the employee — which comes to
// 290.99. This package carries one combined rate per party, so a single
// multiplication by 13.78% gives 290.98, and it can only ever produce that.
// The divergence is one cent, on projections rather than filings, and it is
// accepted deliberately: modelling each fund separately would widen the config
// enormously to buy that cent back. At 936.00 and 620.00 the two agree, which
// is why this needed deciding rather than discovering later against a filing.
func TestVectorAtTheCeilingAcceptsOneCent(t *testing.T) {
	p := bulgariaBands()
	v := atGross(p, p.rulesFor(yearMonth{2026, time.July}), 2111.64)

	checkCents(t, v, map[string]int{
		"gross":    211164,
		"employee": 29098, // 290.98 combined; the accountant's per-fund sum is 290.99
		"employer": 39952, // 399.52 combined; the accountant's per-fund sum is 399.53
		"tax":      18207, // 10% of 1820.66
		"net":      163859,
	})
}

// TestVectorAboveTheCeiling is the band that matters, and the mistake it exists
// to catch is invisible below the ceiling: only the CONTRIBUTION base is
// capped. The tax base is actual gross minus actual employee contributions,
// uncapped, so at 2500 against a 2300 ceiling it is 2183.06 and not 1983.06.
//
// It also pins carry-forward inside a party: the August entry publishes only
// new bands, so July's minimum insurable base is still in force.
func TestVectorAboveTheCeiling(t *testing.T) {
	p := bulgariaBands()
	p.Legislation = append(p.Legislation, LegislationPeriod{
		From:     yearMonth{2026, time.August},
		Employer: &PartySchedule{Bands: Bands{{From: 0, Rate: 0.1892}, {From: 2300, Rate: 0}}},
		Employee: &PartySchedule{Bands: Bands{{From: 0, Rate: 0.1378}, {From: 2300, Rate: 0}}},
	})
	r := p.rulesFor(yearMonth{2026, time.August})
	if r.Employer.MinBase != 620.20 {
		t.Errorf("August minBase = %.2f — an entry that published only bands reset it", r.Employer.MinBase)
	}

	v := atGross(p, r, 2500.00)
	checkCents(t, v, map[string]int{
		"gross":    250000,
		"employee": 31694, // 13.78% of the 2300 ceiling, not of 2500
		"employer": 43516, // 18.92% of 2300
		"tax":      21831, // 10% of 2183.06 — gross is NOT capped
	})
}

// TestVectorTheMinimumWageBinding: the company can afford 300 and the floor is
// 620.20, so the salary is the floor and the difference is owed anyway. This is
// the one vector that goes through the affordability arithmetic rather than
// being handed its gross.
func TestVectorTheMinimumWageBinding(t *testing.T) {
	p := bulgariaBands()
	v := p.breakdown(300, 0, 1, p.rulesFor(yearMonth{2026, time.July}), SalaryFull)

	if !v.MinimumEnforced {
		t.Fatal("the floor bound and the view does not say so")
	}
	checkCents(t, v, map[string]int{
		"gross":    62020,
		"employee": 8546,  // 13.78% of 620.20
		"employer": 11734, // 18.92% of 620.20
	})
	if cost := v.GrossSalaryCents + v.EmployerContribCents; cost != 73754 {
		t.Errorf("total employer cost = %d, want 73754", cost)
	}
}

// TestVectorUKTwoPartiesTwoThresholds is the regression test, because it fails
// three separate ways under one employer rate, one employee rate and one shared
// ceiling: the parties' thresholds differ (£5,000 against £12,570), the
// employee rate does not stop at the upper limit but drops to 2%, and the
// employer rate is not capped at all.
func TestVectorUKTwoPartiesTwoThresholds(t *testing.T) {
	p := PersonalParams{Legislation: Legislation{{
		From: yearMonth{2026, time.April},
		Employee: schedule(0, Band{From: 0, Rate: 0}, Band{From: 12570, Rate: 0.08},
			Band{From: 50270, Rate: 0.02}),
		Employer:  schedule(0, Band{From: 0, Rate: 0}, Band{From: 5000, Rate: 0.15}),
		IncomeTax: Bands{{From: 0, Rate: 0}},
	}}}
	// Annual figures run through one period; the arithmetic does not care what
	// the period is called.
	v := atGross(p, p.rulesFor(yearMonth{2026, time.April}), 60000)

	checkCents(t, v, map[string]int{
		"employee": 321060, // 8% of (50,270-12,570) + 2% of (60,000-50,270)
		"employer": 825000, // 15% of (60,000-5,000), uncapped
	})
}

// TestVectorZeroedPeriod: explicitly zeroed bands must stay distinguishable
// from absent ones, or a period that legislates nothing away looks the same as
// one nobody has written down yet.
func TestVectorZeroedPeriod(t *testing.T) {
	zeroed, err := ParseLegislation([]LegislationEntry{{
		From: "2026-01",
		Contributions: &ContributionsEntry{
			Employer: &PartyEntry{Bands: []BandEntry{band(0, 0)}},
			Employee: &PartyEntry{Bands: []BandEntry{band(0, 0)}},
		},
		IncomeTax: &TaxEntry{Bands: []BandEntry{band(0, 0)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if zeroed[0].Employer.Bands == nil || zeroed[0].IncomeTax == nil {
		t.Fatal("an explicit zero parsed as absent — the whole period would then carry the previous one forward")
	}

	p := PersonalParams{Legislation: zeroed}
	v := p.breakdown(5000, 0, 1, p.rulesFor(yearMonth{2026, time.March}), SalaryFull)
	if v.EmployerContribCents != 0 || v.EmployeeContribCents != 0 || v.IncomeTaxCents != 0 {
		t.Errorf("a zeroed period charged something: %+v", v)
	}
	if v.GrossSalaryCents != 500000 || v.NetIncomeCents != 500000 {
		t.Errorf("gross %d net %d, want the whole 500000 on both", v.GrossSalaryCents, v.NetIncomeCents)
	}
	if v.MinimumEnforced {
		t.Error("a floor was applied by a period that never named one")
	}
}

// TestBandsAreMarginal: a rate applies only to the slice of the base inside its
// own band. This is the property that makes a ceiling a rate: 0 band instead of
// a concept of its own.
func TestBandsAreMarginal(t *testing.T) {
	uk := Bands{{From: 0, Rate: 0}, {From: 12570, Rate: 0.08}, {From: 50270, Rate: 0.02}}
	capped := Bands{{From: 0, Rate: 0.1892}, {From: 2111.64, Rate: 0}}

	for _, c := range []struct {
		name string
		b    Bands
		base float64
		want float64
	}{
		{"below the first boundary", uk, 10000, 0},
		{"straddling one boundary", uk, 20000, 0.08 * 7430},
		{"straddling two", uk, 60000, 3016 + 194.60},
		{"exactly on a boundary", uk, 50270, 3016},
		{"below a ceiling", capped, 1000, 189.20},
		{"above a ceiling", capped, 10000, 0.1892 * 2111.64},
		{"zero base", capped, 0, 0},
	} {
		if got := c.b.on(c.base); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: on(%.2f) = %.6f, want %.6f", c.name, c.base, got, c.want)
		}
	}
}

// TestGrossAffordableInvertsCostExactly is the property that replaced the old
// one-line division. Whatever a salary costs, solving that cost back has to
// land on a salary costing the same — across every schedule shape, including
// the ones a single rate and a single ceiling could not express.
func TestGrossAffordableInvertsCostExactly(t *testing.T) {
	for _, s := range []struct {
		name string
		r    PartyRules
	}{
		{"bulgaria, minBase and a ceiling", PartyRules{MinBase: 620.20, Bands: Bands{{From: 0, Rate: 0.1892}, {From: 2111.64, Rate: 0}}}},
		{"uk employer, uncapped above a threshold", PartyRules{Bands: Bands{{From: 0, Rate: 0}, {From: 5000, Rate: 0.15}}}},
		{"uk employee, three bands", PartyRules{Bands: Bands{{From: 0, Rate: 0}, {From: 12570, Rate: 0.08}, {From: 50270, Rate: 0.02}}}},
		{"one flat rate", PartyRules{Bands: Bands{{From: 0, Rate: 0.1892}}}},
		{"nothing charged at all", PartyRules{Bands: Bands{{From: 0, Rate: 0}}}},
	} {
		for _, gross := range []float64{0, 1, 500, 620.20, 1000, 2111.64, 2500, 5000, 12570, 50270, 60000, 100000} {
			cost := s.r.cost(gross)
			back := s.r.grossAffordable(cost)
			if math.Abs(s.r.cost(back)-cost) > 1e-6 {
				t.Errorf("%s: a salary of %.2f costs %.4f, which solves back to %.4f costing %.4f",
					s.name, gross, cost, back, s.r.cost(back))
			}
		}
	}
}

// TestTheLedgerColumnSubtractsExactly: the Expenses panel is a subtraction
// column, so company income less company expenses less the employer's
// contribution has to BE the gross salary, in cents, with no cent left over
// anywhere the reader can see it.
func TestTheLedgerColumnSubtractsExactly(t *testing.T) {
	p := bulgariaBands()
	r := p.rulesFor(yearMonth{2026, time.July})
	for _, income := range []float64{800, 1000, 1234.56, 2000, 2511.16, 5000, 10000} {
		v := p.breakdown(income, 0, 1, r, SalaryFull)
		if v.MinimumEnforced {
			continue // the floor deliberately breaks the identity; that is what the gap says
		}
		if v.CompanyIncomeCents-v.CompanyExpensesCents-v.EmployerContribCents != v.GrossSalaryCents {
			t.Errorf("income %.2f: %d - %d - %d != %d", income, v.CompanyIncomeCents,
				v.CompanyExpensesCents, v.EmployerContribCents, v.GrossSalaryCents)
		}
		if v.GrossSalaryCents-v.EmployeeContribCents-v.IncomeTaxCents != v.NetIncomeCents {
			t.Errorf("income %.2f: gross - employee - tax != net", income)
		}
	}
}

// rateOf flattens a rate list for assertions.
func rateOf(lines []RateLine) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		if l.Span != "" {
			parts = append(parts, l.Rate+" ["+l.Span+"]")
			continue
		}
		parts = append(parts, l.Rate)
	}
	return strings.Join(parts, " | ")
}

// TestRatesLabelWhatWasCharged: a salary under the ceiling was charged the rate
// in the file and nothing bounded it, so the label is the bare rate. Above the
// ceiling the same rate was charged only up to it, and the label says so —
// where the old blended percentage reported a number that appears in no law.
func TestRatesLabelWhatWasCharged(t *testing.T) {
	p := bulgariaBands()
	r := p.rulesFor(yearMonth{2026, time.July})

	ordinary := atGross(p, r, 1000)
	if got := rateOf(ordinary.EmployerRate); got != "18.92%" {
		t.Errorf("employer rate = %q, want a bare 18.92%% — nothing bounded it", got)
	}
	if got := rateOf(ordinary.EmployeeRate); got != "13.78%" {
		t.Errorf("employee rate = %q", got)
	}
	if got := rateOf(ordinary.IncomeTaxRate); got != "10%" {
		t.Errorf("income tax rate = %q", got)
	}

	rich := atGross(p, r, 10000)
	if got := rateOf(rich.EmployerRate); got != "18.92% up to 2,112" {
		t.Errorf("employer rate = %q, want the rate and the ceiling that bound it", got)
	}
	// The ceiling band charges nothing above itself, and "up to 2,112" has
	// already said where it stopped.
	if strings.Contains(rateOf(rich.EmployerRate), "0%") {
		t.Errorf("the zero-rate ceiling band was printed: %q", rateOf(rich.EmployerRate))
	}
}

// TestMinBaseRaisesTheBaseButDoesNotInventAPayroll: the minimum insurable
// income is charged on a salary that is actually paid. A company with no income
// pays no salary, and a payroll nobody is on owes nothing.
func TestMinBaseRaisesTheBaseButDoesNotInventAPayroll(t *testing.T) {
	r := PartyRules{MinBase: 620.20, Bands: Bands{{From: 0, Rate: 0.1892}}}

	if got := r.on(400); math.Abs(got-0.1892*620.20) > 1e-9 {
		t.Errorf("on(400) = %.4f, want the contribution on the 620.20 minimum base", got)
	}
	if got := r.on(0); got != 0 {
		t.Errorf("on(0) = %.4f — nobody is on the payroll and it owes contributions anyway", got)
	}
	if got := r.grossAffordable(50); got != 0 {
		t.Errorf("grossAffordable(50) = %.4f, want 0 — 50 cannot fund a salary whose contribution alone is 117.34", got)
	}
}

// TestRetiredKeysAreRejectedByName: a half-migrated file must not run. v0.15.0's
// fallback to built-in defaults means it would report plausible wrong numbers,
// which is worse than not starting.
func TestRetiredKeysAreRejectedByName(t *testing.T) {
	for name, e := range map[string]LegislationEntry{
		"socialEmployerRate":        {From: "2026-01", SocialEmployerRate: f64(0.1892), SocialMaxInsurableMonthly: f64(2112)},
		"socialEmployeeRate":        {From: "2026-01", SocialEmployeeRate: f64(0.1378)},
		"socialMaxInsurableMonthly": {From: "2026-01", SocialMaxInsurableMonthly: f64(2112)},
		"incomeTaxRate":             {From: "2026-01", IncomeTaxRate: f64(0.10)},
	} {
		_, err := ParseLegislation([]LegislationEntry{e})
		if err == nil {
			t.Errorf("%s was accepted and silently ignored", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: the error never names the key: %v", name, err)
		}
		if !strings.Contains(err.Error(), "bands") {
			t.Errorf("%s: the error never says what to write instead: %v", name, err)
		}
	}

	// And the message carries the entry's own figures, so the replacement can be
	// pasted rather than worked out.
	_, err := ParseLegislation([]LegislationEntry{
		{From: "2026-01", SocialEmployerRate: f64(0.1892), SocialMaxInsurableMonthly: f64(2112)},
	})
	for _, want := range []string{"0.1892", "2112"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the migration message omits %s: %v", want, err)
		}
	}
}

func TestBandValidation(t *testing.T) {
	bad := map[string][]LegislationEntry{
		"not starting at zero": {{From: "2026-01", Contributions: employerEntry(band(100, 0.1))}},
		"descending":           {{From: "2026-01", Contributions: employerEntry(band(0, 0.1), band(50, 0.2), band(50, 0.3))}},
		"a negative boundary":  {{From: "2026-01", Contributions: employerEntry(band(0, 0.1), band(-5, 0.2))}},
		"a band with no rate":  {{From: "2026-01", Contributions: employerEntry(BandEntry{From: 0})}},
		"a negative minBase": {{From: "2026-01", Contributions: &ContributionsEntry{
			Employer: &PartyEntry{MinBase: f64(-1), Bands: []BandEntry{band(0, 0.1)}}}}},
		"a party stating nothing": {{From: "2026-01", Contributions: &ContributionsEntry{Employer: &PartyEntry{}}}},
		"neither party":           {{From: "2026-01", Contributions: &ContributionsEntry{}}},
		"tax with no bands":       {{From: "2026-01", IncomeTax: &TaxEntry{}}},
	}
	for name, entries := range bad {
		if _, err := ParseLegislation(entries); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// minBase alone is a legitimate statement: the МОД moves without the rates.
	ok, err := ParseLegislation([]LegislationEntry{
		{From: "2026-01", Contributions: &ContributionsEntry{Employer: &PartyEntry{MinBase: f64(700)}}},
	})
	if err != nil {
		t.Fatalf("an entry raising only the minimum insurable base was refused: %v", err)
	}
	if ok[0].Employer.Bands != nil {
		t.Error("an entry that named no bands invented some")
	}
}

// TestBandsReadBackAsProse is what /info shows. A legal obligation you cannot
// see is one you cannot verify, and the file has no thousands separators
// either, so neither does this.
func TestBandsReadBackAsProse(t *testing.T) {
	for _, c := range []struct {
		b    Bands
		want string
	}{
		{Bands{{From: 0, Rate: 0.1892}, {From: 2111.64, Rate: 0}}, "18.92% to 2111.64, then 0%"},
		{Bands{{From: 0, Rate: 0}, {From: 12570, Rate: 0.08}, {From: 50270, Rate: 0.02}}, "0% to 12570, 8% to 50270, then 2%"},
		{Bands{{From: 0, Rate: 0.10}}, "10%"},
	} {
		if got := c.b.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}

	p := LegislationPeriod{
		From:        yearMonth{2026, time.July},
		MinimumWage: f64(620.20),
		Employer:    schedule(620.20, Band{From: 0, Rate: 0.1892}, Band{From: 2111.64, Rate: 0}),
		IncomeTax:   Bands{{From: 0, Rate: 0.10}},
	}
	want := "2026-07: minimumWage 620.2, employer minBase 620.2, employer 18.92% to 2111.64, then 0%, tax 10%"
	if got := p.String(); got != want {
		t.Errorf("period reads as\n %q\nwant\n %q", got, want)
	}
}

// TestScheduleReplacesRatherThanPatches: a band list is one indivisible
// statement. Merging a new schedule into an old one band at a time has no
// meaning a legislature would recognise.
func TestScheduleReplacesRatherThanPatches(t *testing.T) {
	p := PersonalParams{Legislation: Legislation{
		{From: yearMonth{2026, time.January}, Employer: schedule(0, Band{From: 0, Rate: 0.1892}, Band{From: 2112, Rate: 0})},
		{From: yearMonth{2026, time.July}, Employer: schedule(0, Band{From: 0, Rate: 0.15})},
	}}
	after := p.rulesFor(yearMonth{2026, time.July}).Employer.Bands
	if !reflect.DeepEqual(after, Bands{{From: 0, Rate: 0.15}}) {
		t.Errorf("July's schedule = %v, want only its own single band — the old ceiling survived", after)
	}
}

// TestAppliedNamesTheBoundaryThatBoundIt walks the shapes the label has to
// survive, because it is the one number on the page that claims to describe
// the law.
func TestAppliedNamesTheBoundaryThatBoundIt(t *testing.T) {
	capped := Bands{{From: 0, Rate: 0.1892}, {From: 2112, Rate: 0}}
	uk := Bands{{From: 0, Rate: 0}, {From: 12570, Rate: 0.08}, {From: 50270, Rate: 0.02}}

	for _, tt := range []struct {
		name    string
		bands   Bands
		base    float64
		minBase float64
		want    string
	}{
		{"under the ceiling, nothing bound it", capped, 1000, 0, "18.92%"},
		{"exactly at the ceiling", capped, 2112, 0, "18.92%"},
		{"over the ceiling", capped, 10000, 0, "18.92% up to 2,112"},
		// A real top rate is not a ceiling and still has to show.
		{"a top rate that is not zero", uk, 60000, 0, "0% up to 12,570, 8% up to 50,270, then 2%"},
		{"stopped in the middle band", uk, 20000, 0, "0% up to 12,570, then 8%"},
		// The charge was levied on the raised base, not on what was paid.
		{"raised to a minimum base", capped, 400, 933, "18.92% on a 933 minimum base"},
		{"nothing paid, nothing charged", capped, 0, 933, ""},
		{"no schedule at all", nil, 1000, 0, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bands.applied(tt.base, tt.minBase); got != tt.want {
				t.Errorf("applied(%v, %v) = %q, want %q", tt.base, tt.minBase, got, tt.want)
			}
		})
	}
}

// TestARangeShowsALinePerSchedule: a year the law changed in has no single
// rate, and printing one is how the change goes unnoticed by the person
// paying it.
func TestARangeShowsALinePerSchedule(t *testing.T) {
	p := params()
	p.Legislation = Legislation{
		{From: yearMonth{2026, time.January}, Employer: cappedAt(0.1892, 2000),
			Employee: cappedAt(0.1378, 2000), IncomeTax: Bands{{From: 0, Rate: 0.10}}},
		{From: yearMonth{2026, time.May}, Employer: cappedAt(0.20, 3000)},
	}

	year := p.breakdownMonths(make([]float64, 12), nil, yearMonth{2026, time.January})
	if got := rateOf(year.EmployerRate); got != "18.92% up to 2,000 [Jan–Apr] | 20% up to 3,000 [May–Dec]" {
		t.Errorf("employer rate = %q, want a line per schedule with its months", got)
	}
	// The employee schedule never changed, so it stays one line and says
	// nothing about months — repeating "Jan–Dec" on the year page is noise.
	if got := rateOf(year.EmployeeRate); got != "13.78% up to 2,000" {
		t.Errorf("employee rate = %q, want one unlabelled line", got)
	}

	// A single month names no span either.
	month := p.breakdown(1000, 0, 1, p.rulesFor(yearMonth{2026, time.June}), SalaryFull)
	if got := rateOf(month.EmployerRate); got != "20%" {
		t.Errorf("June = %q, want the bare rate a 1000 salary was charged", got)
	}
}

// TestASingleMonthRangeReportsWhatWasCharged: the dashboard's salary block runs
// every single month through breakdownMonths, because the funding shift asks
// for one month at a time. A range of one is still a month and has a base, so
// it must report what was actually charged rather than describing the schedule
// — otherwise every month page claims a ceiling its salary never reached.
func TestASingleMonthRangeReportsWhatWasCharged(t *testing.T) {
	p := params()
	p.Legislation = Legislation{{
		From: yearMonth{2026, time.January}, Employer: cappedAt(0.1892, 2000),
		Employee: cappedAt(0.1378, 2000), IncomeTax: Bands{{From: 0, Rate: 0.10}},
	}}

	// 1,200 of income buys a salary nowhere near the 2,000 ceiling.
	got := p.breakdownMonths([]float64{1200}, nil, yearMonth{2026, time.August})
	if rate := rateOf(got.EmployerRate); rate != "18.92%" {
		t.Errorf("employer rate = %q, want the bare rate — the ceiling was never reached", rate)
	}
	if got.GrossSalaryCents >= 200000 {
		t.Fatalf("gross = %d, which does reach the ceiling — this test proves nothing", got.GrossSalaryCents)
	}

	// And a month that does clear it still names it.
	rich := p.breakdownMonths([]float64{20000}, nil, yearMonth{2026, time.August})
	if rate := rateOf(rich.EmployerRate); rate != "18.92% up to 2,000" {
		t.Errorf("employer rate = %q, want the ceiling that bound it", rate)
	}
}
