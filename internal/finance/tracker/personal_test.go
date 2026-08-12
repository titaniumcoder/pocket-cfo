package tracker

import (
	"testing"
	"time"
)

// fromTheStart dates a period meant to be in force in every month a test looks
// at. Year one precedes every payroll month there will ever be; it is an
// ordinary date, writable in config.json as "0001-01", not a special value.
var fromTheStart = yearMonth{1, time.January}

// params is the Bulgarian figures as one always-in-force period — what every
// test written before dated legislation assumed, and still means.
func params() PersonalParams { return testLegislation(0.1892, 0.1378, 2112, 0.10) }

func testParams() PersonalParams { return params() }

// testLegislation wraps four rates as a single period in force from the start
// of time. It keeps the pre-bands vocabulary on purpose — a ceiling is now a
// band with a rate of zero, and translating it here rather than in forty call
// sites is what lets every test written before bands keep saying what it meant.
func testLegislation(employer, employee, maxInsurable, tax float64) PersonalParams {
	return PersonalParams{Legislation: Legislation{{
		From:      fromTheStart,
		Employer:  cappedAt(employer, maxInsurable),
		Employee:  cappedAt(employee, maxInsurable),
		IncomeTax: Bands{{From: 0, Rate: tax}},
	}}}
}

// cappedAt is one rate up to a ceiling and nothing above it — Bulgaria's shape.
func cappedAt(rate, ceiling float64) *PartySchedule {
	return &PartySchedule{Bands: Bands{{From: 0, Rate: rate}, {From: ceiling, Rate: 0}}}
}

func TestBreakdownCappedSalary(t *testing.T) {
	p := params()

	// €10,000/month company income, no company expenses, one month.
	v := p.breakdown(10000, 0, 1, p.rulesFor(testMonth), SalaryFull)

	// salary above cap so employer contrib is flat.
	// gross = 10000 - 0.1892*2112 = 9600.41
	want := map[string]int{
		"company": 1000000,
		"erSoc":   39959, // 0.1892 * 2112
		"gross":   960041,
		"eeSoc":   29103, // 0.1378 * 2112
		"tax":     93094, // 0.10 * (9600.41 - 291.03)
		"net":     837844,
	}
	got := map[string]int{
		"company": v.CompanyIncomeCents,
		"erSoc":   v.EmployerContribCents,
		"gross":   v.GrossSalaryCents,
		"eeSoc":   v.EmployeeContribCents,
		"tax":     v.IncomeTaxCents,
		"net":     v.NetIncomeCents,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %d, want %d", k, got[k], w)
		}
	}
	if v.CompanyExpensesCents != 0 {
		t.Errorf("CompanyExpensesCents = %d, want 0", v.CompanyExpensesCents)
	}

	// Waterfall identities must hold exactly (in cents).
	if v.CompanyIncomeCents-v.CompanyExpensesCents-v.EmployerContribCents != v.GrossSalaryCents {
		t.Errorf("company - companyExpenses - employer != gross")
	}
	if v.GrossSalaryCents-v.EmployeeContribCents-v.IncomeTaxCents != v.NetIncomeCents {
		t.Errorf("gross - employee - tax != net")
	}
}

func TestBreakdownYearScalesByTwelve(t *testing.T) {
	p := params()

	month := p.breakdown(10000, 0, 1, p.rulesFor(testMonth), SalaryFull)
	year := p.breakdown(120000, 0, 12, p.rulesFor(testMonth), SalaryFull) // same €10,000/month smoothed over a year

	// Year figures are the per-month figures times twelve, give or take a couple
	// of cents of rounding (the annual total is rounded once, not summed).
	nearly := func(name string, got, per int) {
		if diff := got - 12*per; diff < -6 || diff > 6 {
			t.Errorf("year %s %d not ~12x month %d (diff %d)", name, got, per, diff)
		}
	}
	nearly("net", year.NetIncomeCents, month.NetIncomeCents)
	nearly("employer contrib", year.EmployerContribCents, month.EmployerContribCents)
}

func TestBreakdownBelowCap(t *testing.T) {
	p := params()

	// Salary stays under the cap, so employer contrib is a real percentage of gross.
	v := p.breakdown(1000, 0, 1, p.rulesFor(testMonth), SalaryFull)
	// gross = 1000 / 1.1892 = 840.90; under cap.
	if v.GrossSalaryCents != 84090 {
		t.Errorf("gross = %d, want 84090", v.GrossSalaryCents)
	}
	if v.CompanyIncomeCents-v.EmployerContribCents != v.GrossSalaryCents {
		t.Errorf("waterfall identity broken below cap")
	}
}

func TestBreakdownZeroIncome(t *testing.T) {
	p := params()

	v := p.breakdown(0, 0, 1, p.rulesFor(testMonth), SalaryFull)
	if v.GrossSalaryCents != 0 || v.EmployerContribCents != 0 || v.NetIncomeCents != 0 {
		t.Errorf("salary should be zero for zero company income: %+v", v)
	}
}

// TestBreakdownCompanyExpensesDeductedFirst confirms company expenses come
// off company income before the salary cascade — the cascade should behave
// exactly as if the smaller (post-expenses) amount had been the company
// income all along, not as if expenses were subtracted from net income
// afterward.
func TestBreakdownCompanyExpensesDeductedFirst(t *testing.T) {
	p := params()

	// €1,000 company income, €200 company expenses -> same cascade as €800
	// company income with no expenses.
	withExpenses := p.breakdown(1000, 200, 1, p.rulesFor(testMonth), SalaryFull)
	equivalent := p.breakdown(800, 0, 1, p.rulesFor(testMonth), SalaryFull)

	if withExpenses.GrossSalaryCents != equivalent.GrossSalaryCents {
		t.Errorf("GrossSalaryCents = %d, want %d (same as €800 income, no expenses)", withExpenses.GrossSalaryCents, equivalent.GrossSalaryCents)
	}
	if withExpenses.NetIncomeCents != equivalent.NetIncomeCents {
		t.Errorf("NetIncomeCents = %d, want %d", withExpenses.NetIncomeCents, equivalent.NetIncomeCents)
	}
	// Company income itself still reports the raw (pre-expenses) figure, for display.
	if want := eurToCents(1000); withExpenses.CompanyIncomeCents != want {
		t.Errorf("CompanyIncomeCents = %d, want %d (raw, not net of expenses)", withExpenses.CompanyIncomeCents, want)
	}
	if want := eurToCents(200); withExpenses.CompanyExpensesCents != want {
		t.Errorf("CompanyExpensesCents = %d, want %d", withExpenses.CompanyExpensesCents, want)
	}
}

// TestBreakdownCompanyExpensesExceedingIncomeFloorsAtZero confirms company
// expenses larger than company income don't produce a negative salary.
func TestBreakdownCompanyExpensesExceedingIncomeFloorsAtZero(t *testing.T) {
	p := params()
	v := p.breakdown(500, 2000, 1, p.rulesFor(testMonth), SalaryFull)
	if v.GrossSalaryCents != 0 || v.EmployerContribCents != 0 || v.NetIncomeCents != 0 {
		t.Errorf("expected a zero salary when company expenses exceed company income: %+v", v)
	}
}

func TestBreakdownMonthsHandlesMixedYear(t *testing.T) {
	p := params()

	year := p.breakdownMonthsNoFloor([]float64{
		0,     // empty month
		1000,  // below social max insurable: uncapped percentage contributions
		3000,  // below social max insurable: uncapped percentage contributions
		10000, // above social max insurable: capped contributions
	}, nil)

	empty := p.breakdown(0, 0, 1, p.rulesFor(testMonth), SalaryFull)
	low := p.breakdown(1000, 0, 1, p.rulesFor(testMonth), SalaryFull)
	belowCap := p.breakdown(3000, 0, 1, p.rulesFor(testMonth), SalaryFull)
	aboveCap := p.breakdown(10000, 0, 1, p.rulesFor(testMonth), SalaryFull)

	wantGross := empty.GrossSalaryCents + low.GrossSalaryCents + belowCap.GrossSalaryCents + aboveCap.GrossSalaryCents
	wantNet := empty.NetIncomeCents + low.NetIncomeCents + belowCap.NetIncomeCents + aboveCap.NetIncomeCents

	if year.CompanyIncomeCents != 1400000 {
		t.Errorf("company = %d, want 1400000", year.CompanyIncomeCents)
	}
	if year.GrossSalaryCents != wantGross {
		t.Errorf("gross = %d, want %d", year.GrossSalaryCents, wantGross)
	}
	if year.NetIncomeCents != wantNet {
		t.Errorf("net = %d, want %d", year.NetIncomeCents, wantNet)
	}
}

// TestBreakdownMonthsAppliesPerMonthCompanyExpenses confirms each month's
// company expenses only affect that month's cascade (matters for the
// monthly social-insurance cap), and a nil/short expenses slice treats
// missing months as zero.
func TestBreakdownMonthsAppliesPerMonthCompanyExpenses(t *testing.T) {
	p := params()

	year := p.breakdownMonthsNoFloor(
		[]float64{1000, 1000, 1000},
		[]float64{200, 0}, // third month gets zero (slice too short)
	)

	m1 := p.breakdown(1000, 200, 1, p.rulesFor(testMonth), SalaryFull)
	m2 := p.breakdown(1000, 0, 1, p.rulesFor(testMonth), SalaryFull)
	m3 := p.breakdown(1000, 0, 1, p.rulesFor(testMonth), SalaryFull)

	wantNet := m1.NetIncomeCents + m2.NetIncomeCents + m3.NetIncomeCents
	if year.NetIncomeCents != wantNet {
		t.Errorf("NetIncomeCents = %d, want %d", year.NetIncomeCents, wantNet)
	}
	wantCompanyExpenses := m1.CompanyExpensesCents + m2.CompanyExpensesCents + m3.CompanyExpensesCents
	if year.CompanyExpensesCents != wantCompanyExpenses {
		t.Errorf("CompanyExpensesCents = %d, want %d", year.CompanyExpensesCents, wantCompanyExpenses)
	}
}

// breakdownMonthsNoFloor runs the cascade with no statutory minimum, which is
// what every test written before there was one assumed. Kept as a shim so
// those tests still say what they meant rather than growing an argument that
// is always the same.
func (p PersonalParams) breakdownMonthsNoFloor(monthlyIncomeEUR, monthlyCompanyExpensesEUR []float64) PersonalView {
	return p.breakdownMonths(monthlyIncomeEUR, monthlyCompanyExpensesEUR, yearMonth{2026, time.January})
}

// testMonth is a month no fixture legislates for, so rulesFor returns the
// plain baseline rates — which is what every test written before dated
// legislation assumed, and still means.
var testMonth = yearMonth{2026, time.January}
