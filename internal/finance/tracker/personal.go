package tracker

import (
	"math"
	"reflect"
)

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
	// than what the company could afford.
	MinimumWageCents int
	MinimumEnforced  bool

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

	// What each deduction was charged at — see Bands.applied. A list because a
	// range that spans a change in the law has no single answer, and reporting
	// the last month's rate for the whole year is how a change goes unnoticed.
	// One entry with no Span for a single month.
	EmployerRate  []RateLine
	EmployeeRate  []RateLine
	IncomeTaxRate []RateLine
}

// RateLine is one schedule and the months it applied to. Span is empty when
// there is only one, which is every single month and most years.
type RateLine struct {
	Rate string
	Span string // "Jan–Apr"
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

// oneRate is the single-month case: what this schedule charged on this base,
// with no span to name because there is only one.
func oneRate(base, minBase float64, b Bands) []RateLine {
	rate := b.applied(base, minBase)
	if rate == "" {
		return nil
	}
	return []RateLine{{Rate: rate}}
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

	m := float64(months)
	cents := func(x float64) int { return round(x * 100 * m) }
	result.EmployerRate = oneRate(gross, r.Employer.MinBase, r.Employer.Bands)
	result.EmployeeRate = oneRate(gross, r.Employee.MinBase, r.Employee.Bands)
	// The tax base is what was taxed, and it is never raised to a minimum base
	// — only contribution bases are.
	result.IncomeTaxRate = oneRate(taxable, 0, r.IncomeTax)
	result.MinimumWageCents = round(r.MinimumEUR * 100)
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
	var one PersonalView
	for i, income := range monthlyIncomeEUR {
		var companyExpenses float64
		if i < len(monthlyCompanyExpensesEUR) {
			companyExpenses = monthlyCompanyExpensesEUR[i]
		}
		// Each month gets the figures that were in force in it, so a year
		// spanning a January increase is summed against both rather than
		// against one of them twice.
		m := p.breakdown(income, companyExpenses, 1, p.rulesFor(start.addMonths(i)))
		one = m
		if m.MinimumEnforced {
			result.MinimumEnforced = true
		}
		if m.NoLegislation {
			result.NoLegislation = true
		}
		if m.MinimumWageCents > result.MinimumWageCents {
			result.MinimumWageCents = m.MinimumWageCents
		}
		result.CompanyIncomeCents += m.CompanyIncomeCents
		result.CompanyExpensesCents += m.CompanyExpensesCents
		result.EmployerContribCents += m.EmployerContribCents
		result.GrossSalaryCents += m.GrossSalaryCents
		result.EmployeeContribCents += m.EmployeeContribCents
		result.IncomeTaxCents += m.IncomeTaxCents
		result.NetIncomeCents += m.NetIncomeCents
	}
	// A range gets one line per schedule that was in force during it, labelled
	// with the months it covered. Reporting a single rate for a year the law
	// changed in is how a change goes unnoticed by the person paying it.
	//
	// These lines describe the schedule, not the bands one salary reached: a
	// range has a base per month, so "what was charged" has no single answer
	// here the way it does for a month.
	months := len(monthlyIncomeEUR)
	if months == 1 {
		// A range of one is still a month, and a month has a base — so it gets
		// the sharper answer, what was actually charged on it. This is not a
		// rare path: the dashboard's salary block runs through here for every
		// single month, because the funding shift asks for one month at a time.
		result.EmployerRate, result.EmployeeRate, result.IncomeTaxRate = one.EmployerRate, one.EmployeeRate, one.IncomeTaxRate
		return result
	}
	result.EmployerRate = p.rateLines(start, months, func(r Rules) (Bands, float64) {
		return r.Employer.Bands, r.Employer.MinBase
	})
	result.EmployeeRate = p.rateLines(start, months, func(r Rules) (Bands, float64) {
		return r.Employee.Bands, r.Employee.MinBase
	})
	result.IncomeTaxRate = p.rateLines(start, months, func(r Rules) (Bands, float64) {
		return r.IncomeTax, 0
	})
	return result
}

// rateLines walks the months and emits one line per run of consecutive months
// sharing a schedule, so a year that changed in May reads "18.92% up to 2,112
// Jan–Apr" and "20% up to 3,000 May–Dec" rather than one of the two.
//
// A run is described by its own schedule at the top of its band range, which
// for a monthly threshold is the schedule itself — the point of the label is
// which rules applied when, not what one month's salary happened to reach.
func (p PersonalParams) rateLines(start yearMonth, months int, pick func(Rules) (Bands, float64)) []RateLine {
	if months <= 0 {
		return nil
	}
	var out []RateLine
	runStart := 0
	var runBands Bands
	var runMin float64
	flush := func(from, to int) {
		text := describeSchedule(runBands, runMin)
		if text == "" {
			return
		}
		out = append(out, RateLine{Rate: text, Span: spanLabel(start, from, to)})
	}
	for i := 0; i < months; i++ {
		bands, minBase := pick(p.rulesFor(start.addMonths(i)))
		if i == 0 {
			runBands, runMin = bands, minBase
			continue
		}
		if reflect.DeepEqual(bands, runBands) && minBase == runMin {
			continue
		}
		flush(runStart, i-1)
		runStart, runBands, runMin = i, bands, minBase
	}
	flush(runStart, months-1)
	if len(out) == 1 {
		// One schedule all range: naming the span would only repeat the period
		// the whole page is already about.
		out[0].Span = ""
	}
	return out
}

// describeSchedule renders a schedule without reference to any one base — the
// range case, where there is a base per month.
//
// It asks applied() about a base just past the last boundary, so every band is
// passed clean through and every boundary gets named. A base exactly on the
// last boundary would not do: applied() would read it as having come to rest
// inside the band below, which is true of that base and false of the schedule.
func describeSchedule(b Bands, minBase float64) string {
	if len(b) == 0 {
		return ""
	}
	return b.applied(b[len(b)-1].From+1, minBase)
}

// spanLabel names the months a run covered, as "Jan–Apr" or a bare "May" for a
// single one.
func spanLabel(start yearMonth, from, to int) string {
	first := start.addMonths(from).Month.String()[:3]
	if from == to {
		return first
	}
	return first + "–" + start.addMonths(to).Month.String()[:3]
}
