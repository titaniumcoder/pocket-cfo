package tracker

import "math"

// PersonalParams turns company income into net personal income for a one-person
// company paying the owner a salary. Rates are fractions (0.10 = 10%); money is
// in EUR.
//
// Company expenses come off first (see breakdown's companyExpensesEUR), since
// that money never becomes salary; only what's left becomes salary cost.
// Private spending is deducted from Net income elsewhere, not here.
type PersonalParams struct {
	// Legislation is every government-set figure this cascade uses: both
	// parties' contribution schedules, the income tax bands and the statutory
	// minimum wage. There is nothing beside it, because there is no such thing
	// as an undated tax rate — every one of these was set on a date by someone,
	// and keeping some of them undated is what made last year's figures
	// unreproducible.
	Legislation Legislation
}

// PersonalView is the rendered waterfall, in cents. CompanyGroups is filled in
// by Tracker.compute for display; breakdown/breakdownMonths only handle the
// numeric cascade.
type PersonalView struct {
	Err string

	CompanyIncomeCents   int
	CompanyExpensesCents int
	EmployerContribCents int
	GrossSalaryCents     int
	EmployeeContribCents int
	IncomeTaxCents       int
	NetIncomeCents       int

	// MinimumWageCents is the floor in force for the period, and
	// MinimumEnforced says it bound — the salary is the legal minimum rather
	// than what the company could afford. ShortfallCents is what that costs
	// beyond the period's own income, and it is the figure worth seeing: the
	// salary is owed whether or not the month paid for it.
	MinimumWageCents int
	MinimumEnforced  bool
	ShortfallCents   int

	// NoLegislation says no figure was in force for this period at all — the
	// months before the earliest entry, or a config.json that states none.
	// Nothing was contributed or taxed, and since that renders as a payslip
	// with no deductions rather than as an error, the page says so. Over a
	// range it is set if any month in it had nothing in force.
	NoLegislation bool

	CompanyGroups []CategoryGroupView

	// Which period's Company income this cascade came from. Set only on
	// Figures.FundingPersonal, where it differs from the viewed period; blank
	// on Figures.Personal, which is already the period the caller asked about.
	FundingLabel string // e.g. "November 2025"
	FundingURL   string // jumps to that funding period

	// The rate each line actually came out at, as a percentage (e.g. "18.92").
	// A band schedule has no single rate, so these are effective rates — see
	// effectivePct.
	EmployerPct  string
	EmployeePct  string
	IncomeTaxPct string
}

// toCent rounds to the nearest cent. Every line of the cascade goes through it
// before the line below uses the figure, which is what a payslip does: the tax
// base on one is the rounded contribution subtracted from the rounded gross,
// not an unrounded intermediate nobody ever sees. Carrying full precision to
// the end instead moves the net by a cent against the payroll documents.
func toCent(v float64) float64 { return math.Round(v*100) / 100 }

// on is what a party is charged on a salary of gross.
//
// A zero salary owes nothing: MinBase raises the base of a salary that is
// actually paid, it does not invent a payroll where there is none. Without that
// rule a company with no income would owe contributions on a salary it never
// paid.
func (r PartyRules) on(gross float64) float64 {
	if gross <= 0 {
		return 0
	}
	base := gross
	if r.MinBase > base {
		base = r.MinBase
	}
	return r.Bands.on(base)
}

// cost is what a salary of gross costs the company: the salary plus the
// employer's contribution on it.
func (r PartyRules) cost(gross float64) float64 { return gross + r.on(gross) }

// grossAffordable solves for the gross salary whose total cost to the company
// is exactly the money available.
//
// Cost is piecewise linear in gross and never decreasing — every segment's
// slope is 1+rate and no rate is negative — so it inverts exactly by walking
// the band boundaries rather than searching: find the segment whose cost
// bracket contains the money available, then divide by that segment's slope.
//
// One flat rate makes this available/(1+rate), which is what it was before
// bands; a rate: 0 band on top reproduces "the contribution is capped, so the
// rest is salary"; and an uncapped non-zero top band — UK employer NI at 15%
// with no ceiling — stays correct where both of those would silently overpay.
func (r PartyRules) grossAffordable(available float64) float64 {
	if available <= 0 {
		return 0
	}
	if len(r.Bands) == 0 {
		return available
	}
	prevGross, prevCost := 0.0, 0.0
	if r.MinBase > 0 {
		// Below MinBase the contribution does not move — it is charged on
		// MinBase however little is actually paid — so the salary is simply
		// what is left after it, and if that is nothing then nobody is paid.
		flat := r.Bands.on(r.MinBase)
		if available <= flat {
			return 0
		}
		if available <= r.MinBase+flat {
			return available - flat
		}
		prevGross, prevCost = r.MinBase, r.MinBase+flat
	}
	for _, b := range r.Bands {
		if b.From <= prevGross {
			continue
		}
		c := r.cost(b.From)
		if c >= available {
			slope := (c - prevCost) / (b.From - prevGross)
			return prevGross + (available-prevCost)/slope
		}
		prevGross, prevCost = b.From, c
	}
	return prevGross + (available-prevCost)/(1+r.Bands[len(r.Bands)-1].Rate)
}

// effectivePct is the rate a figure actually came out at: what was charged over
// what it was charged on. A schedule with one rate below its ceiling gives back
// exactly the rate in the file, so an ordinary month reads as it always did;
// above a ceiling it reads the rate that was really charged rather than one
// that was not. With nothing to charge on there is no effective rate, and the
// opening band's is the honest answer.
func effectivePct(charged, base float64, b Bands) string {
	if base <= 0 {
		if len(b) == 0 {
			return formatNum(0)
		}
		return formatNum(b[0].Rate * 100)
	}
	return formatNum(charged / base * 100)
}

// breakdown computes the waterfall over `months` months. Salary is smoothed
// evenly so that monthly band boundaries apply correctly, then the per-month
// figures are scaled back up for display.
func (p PersonalParams) breakdown(totalIncomeEUR, companyExpensesEUR float64, months int, r Rules) PersonalView {
	var result PersonalView
	if months <= 0 {
		months = 1
	}
	result.NoLegislation = r.nothingInForce()

	monthlyRawIncome := totalIncomeEUR / float64(months)
	monthlyCompanyExpenses := companyExpensesEUR / float64(months)
	monthlyIncome := toCent(monthlyRawIncome - monthlyCompanyExpenses)

	gross := toCent(r.Employer.grossAffordable(monthlyIncome))
	if gross < 0 {
		gross = 0
	}
	// The floor goes on last, after the affordability arithmetic, because it
	// is not an affordability question: an employed person is owed the
	// statutory minimum whether the company earned it or not. Everything
	// downstream — both contributions, the tax, the net — is then computed on
	// what is actually paid.
	if r.MinimumEUR > 0 && gross < r.MinimumEUR {
		gross = toCent(r.MinimumEUR)
		result.MinimumEnforced = true
	}

	employerContrib := toCent(r.Employer.on(gross))
	if !result.MinimumEnforced && gross > 0 {
		// Take the salary as what is left of the period's money once the
		// employer's contribution is paid, so the ledger column subtracts
		// exactly rather than to within a cent. This moves gross only by the
		// rounding of the contribution itself — grossAffordable already solved
		// the two against each other.
		if g := monthlyIncome - employerContrib; g >= 0 {
			gross = g
		}
	}

	employeeContrib := toCent(r.Employee.on(gross))

	// The tax base is actual gross minus actual employee contributions. Only
	// the contribution base is ever capped, never this one — getting that wrong
	// is invisible below a ceiling and wrong by the ceiling above it.
	taxable := gross - employeeContrib
	if taxable < 0 {
		taxable = 0
	}
	incomeTax := toCent(r.IncomeTax.on(taxable))
	net := gross - employeeContrib - incomeTax

	// What the payroll costs the company against what the period produced —
	// but only reported when the floor is what caused it. Without a floor the
	// salary is solved to fit the income exactly, so any gap is the older
	// "company expenses exceeded company income" case, which the zero salary
	// already says and which this wording would describe wrongly.
	var shortfall float64
	if result.MinimumEnforced {
		shortfall = gross + employerContrib - monthlyIncome
		if shortfall < 0 {
			shortfall = 0
		}
	}

	m := float64(months)
	cents := func(x float64) int { return round(x * 100 * m) }
	result.EmployerPct = effectivePct(employerContrib, gross, r.Employer.Bands)
	result.EmployeePct = effectivePct(employeeContrib, gross, r.Employee.Bands)
	result.IncomeTaxPct = effectivePct(incomeTax, taxable, r.IncomeTax)
	result.MinimumWageCents = round(r.MinimumEUR * 100)
	result.ShortfallCents = cents(shortfall)
	result.CompanyIncomeCents = cents(monthlyRawIncome)
	result.CompanyExpensesCents = cents(monthlyCompanyExpenses)
	result.EmployerContribCents = cents(employerContrib)
	result.GrossSalaryCents = cents(gross)
	result.EmployeeContribCents = cents(employeeContrib)
	result.IncomeTaxCents = cents(incomeTax)
	result.NetIncomeCents = cents(net)
	return result
}

// breakdownMonths runs the waterfall month by month and sums, preserving the
// monthly band boundaries for partial years and uneven income. A short or nil
// monthlyCompanyExpensesEUR treats missing months as zero.
func (p PersonalParams) breakdownMonths(monthlyIncomeEUR, monthlyCompanyExpensesEUR []float64, start yearMonth) PersonalView {
	var result PersonalView
	for i, income := range monthlyIncomeEUR {
		var companyExpenses float64
		if i < len(monthlyCompanyExpensesEUR) {
			companyExpenses = monthlyCompanyExpensesEUR[i]
		}
		// Each month gets the figures that were in force in it, so a year
		// spanning a January increase is summed against both rather than
		// against one of them twice.
		m := p.breakdown(income, companyExpenses, 1, p.rulesFor(start.addMonths(i)))
		if m.MinimumEnforced {
			result.MinimumEnforced = true
		}
		if m.NoLegislation {
			result.NoLegislation = true
		}
		if m.MinimumWageCents > result.MinimumWageCents {
			result.MinimumWageCents = m.MinimumWageCents
		}
		result.ShortfallCents += m.ShortfallCents
		result.CompanyIncomeCents += m.CompanyIncomeCents
		result.CompanyExpensesCents += m.CompanyExpensesCents
		result.EmployerContribCents += m.EmployerContribCents
		result.GrossSalaryCents += m.GrossSalaryCents
		result.EmployeeContribCents += m.EmployeeContribCents
		result.IncomeTaxCents += m.IncomeTaxCents
		result.NetIncomeCents += m.NetIncomeCents
	}
	// The labelled rates are effective over the whole range rather than any one
	// month's. A range spanning a change has no single rate, and what a reader
	// means by "the rate" is the one these totals came out at — which is also
	// the only figure the column beside them supports.
	last := p.rulesFor(start.addMonths(max(0, len(monthlyIncomeEUR)-1)))
	gross := float64(result.GrossSalaryCents)
	result.EmployerPct = effectivePct(float64(result.EmployerContribCents), gross, last.Employer.Bands)
	result.EmployeePct = effectivePct(float64(result.EmployeeContribCents), gross, last.Employee.Bands)
	result.IncomeTaxPct = effectivePct(float64(result.IncomeTaxCents), gross-float64(result.EmployeeContribCents), last.IncomeTax)
	return result
}
