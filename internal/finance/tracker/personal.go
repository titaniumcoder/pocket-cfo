package tracker

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

type PersonalParams struct {
	Legislation Legislation

	Salary SalaryPlan

	Target TargetPlan
}

// decide is the whole salary policy for one month. The salary block states it;
// a target balance can only ever hold back a month that would otherwise pay a
// full salary, and cannot make any month pay more.
func (p PersonalParams) decide(ym yearMonth, stock companyStock) SalaryDecision {
	d := p.Salary.decisionFor(ym)
	if _, inForce := p.Target.at(ym); !inForce {
		return d
	}
	if d.Mode != SalaryFull {
		d.TargetIdle = true
		return d
	}
	if stock.belowTarget() {
		return SalaryDecision{Mode: SalaryMinimum, HeldForTarget: true}
	}
	return d
}

// targetStock reads the target for a month onto the balance carried into it.
// An unknown balance takes no target: there is nothing to compare it against,
// and treating "we were not told" as "the company holds nothing" would hold
// every month at the minimum on no evidence.
func (p PersonalParams) targetStock(ym yearMonth, stock companyStock) companyStock {
	stock.TargetCents = 0
	if !stock.Known {
		return stock
	}
	if amount, inForce := p.Target.at(ym); inForce {
		stock.TargetCents = round(amount * 100)
	}
	return stock
}

type companyStock struct {
	Known        bool
	OpeningCents int
	TargetCents  int
}

// spendableEUR is the target-as-a-floor rule, and it is the whole of it: the
// cascade may spend what the company holds above its target and no more. With
// no target that is everything, which is what a full salary always did.
func (s companyStock) spendableEUR() float64 {
	if !s.Known {
		return 0
	}
	return float64(s.OpeningCents-s.TargetCents) / 100
}

func (s companyStock) belowTarget() bool {
	return s.Known && s.OpeningCents < s.TargetCents
}

type PersonalView struct {
	Err string

	CompanyIncomeCents   int
	CompanyExpensesCents int
	EmployerContribCents int
	GrossSalaryCents     int
	EmployeeContribCents int
	IncomeTaxCents       int
	NetIncomeCents       int

	ShowCompanyBalance  bool
	CompanyOpeningCents int
	CompanyClosingCents int
	CompanyTargetCents  int
	HeldForTarget       bool
	MonthsHeldForTarget int
	TargetIdle          bool

	MinimumWageCents int
	MinimumEnforced  bool

	Mode                SalaryMode
	FixedSalaryCents    int
	MonthsAtMinimum     int
	MonthsAtFixed       int
	MonthsWithoutSalary int

	NoLegislation bool

	CompanyGroups []CategoryGroupView

	FundingLabel string
	FundingURL   string

	EmployerRate  []RateLine
	EmployeeRate  []RateLine
	IncomeTaxRate []RateLine
}

type RateLine struct {
	Rate string
	Span string
}

func toCent(v float64) float64 { return math.Round(v*100) / 100 }

// TargetNote explains a gross salary that matches neither affordability nor a
// figure anyone wrote down: the month was held at the minimum to let the
// company reach a balance it is saving towards. Without it the page shows a
// salary decision with no visible cause.
func (v PersonalView) TargetNote() string {
	switch {
	case v.CompanyTargetCents == 0:
		return ""
	case v.TargetIdle:
		return "A target balance of " + formatEuro(v.CompanyTargetCents) + " is set for this period but does nothing here, because the salary is already fixed by config.json. A target only holds back a month that would otherwise pay a full salary."
	case v.MonthsHeldForTarget > 1:
		return fmt.Sprintf("%d month(s) are held at the statutory minimum until the company reaches %s. Full salary resumes once it does, and never spends below it.",
			v.MonthsHeldForTarget, formatEuro(v.CompanyTargetCents))
	case v.HeldForTarget:
		return "Held at the statutory minimum: the company is at " + formatEuro(v.CompanyOpeningCents) + " and is saving towards " + formatEuro(v.CompanyTargetCents) + ". Full salary resumes once it gets there."
	case v.ShowCompanyBalance:
		return "The company has reached its target of " + formatEuro(v.CompanyTargetCents) + ", so a full salary is drawn again — out of what sits above the target, which stays put."
	}
	return ""
}

// CompanyOverdrawnNote exists because a negative closing balance is otherwise
// just a red figure. It is the expected outcome of a fixed salary the company
// cannot afford — a deliberate choice — and unremarked it reads as a bug.
func (v PersonalView) CompanyOverdrawnNote() string {
	if !v.ShowCompanyBalance || v.CompanyClosingCents >= 0 {
		return ""
	}
	if v.Mode == SalaryFixed {
		return "The fixed salary is more than the company earned, so it ends the month overdrawn. That is what fixing a salary means: it is paid whether or not the money is there."
	}
	return "The company ends the month overdrawn — it paid out more than it took in."
}

func (v PersonalView) MixedMonthsNote() string {
	if v.Mode != "" {
		return ""
	}
	var parts []string
	if v.MonthsAtMinimum > 0 {
		parts = append(parts, fmt.Sprintf("%d month(s) pay only the statutory minimum", v.MonthsAtMinimum))
	}
	if v.MonthsAtFixed > 0 {
		parts = append(parts, fmt.Sprintf("%d month(s) pay a fixed salary", v.MonthsAtFixed))
	}
	if v.MonthsWithoutSalary > 0 {
		parts = append(parts, fmt.Sprintf("%d month(s) draw no salary at all", v.MonthsWithoutSalary))
	}
	if len(parts) == 0 {
		return ""
	}
	return joinClauses(parts) + " — so this total is not twelve full months."
}

func joinClauses(parts []string) string {
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + ", and " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
}

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

func (r PartyRules) cost(gross float64) float64 { return gross + r.on(gross) }

func (r PartyRules) grossAffordable(available float64) float64 {
	if available <= 0 {
		return 0
	}
	if len(r.Bands) == 0 {
		return available
	}
	prevGross, prevCost := 0.0, 0.0
	if r.MinBase > 0 {
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

func oneRate(base, minBase float64, b Bands) []RateLine {
	rate := b.applied(base, minBase)
	if rate == "" {
		return nil
	}
	return []RateLine{{Rate: rate}}
}

func (p PersonalParams) breakdown(totalIncomeEUR, companyExpensesEUR float64, months int, r Rules, d SalaryDecision, stock companyStock) PersonalView {
	if months <= 0 {
		months = 1
	}
	mode := d.Mode
	monthlyRawIncome := totalIncomeEUR / float64(months)
	monthlyCompanyExpenses := companyExpensesEUR / float64(months)
	availableForPayroll := toCent(monthlyRawIncome - monthlyCompanyExpenses + stock.spendableEUR())

	gross, minimumEnforced := grossSalaryFor(d, r, availableForPayroll)
	employerContrib := toCent(r.Employer.on(gross))
	if mode == SalaryFull && !minimumEnforced {
		gross = wholeRemainderAfter(employerContrib, availableForPayroll, gross)
	}
	employeeContrib := toCent(r.Employee.on(gross))
	taxable := taxableAfter(gross, employeeContrib)
	incomeTax := toCent(r.IncomeTax.on(taxable))
	net := gross - employeeContrib - incomeTax

	m := float64(months)
	cents := func(x float64) int { return round(x * 100 * m) }
	v := PersonalView{
		NoLegislation:        r.nothingInForce(),
		Mode:                 mode,
		MinimumEnforced:      minimumEnforced,
		HeldForTarget:        d.HeldForTarget,
		TargetIdle:           d.TargetIdle,
		MinimumWageCents:     round(r.MinimumEUR * 100),
		FixedSalaryCents:     round(d.FixedEUR * 100),
		EmployerRate:         oneRate(gross, r.Employer.MinBase, r.Employer.Bands),
		EmployeeRate:         oneRate(gross, r.Employee.MinBase, r.Employee.Bands),
		IncomeTaxRate:        oneRate(taxable, 0, r.IncomeTax),
		CompanyIncomeCents:   cents(monthlyRawIncome),
		CompanyExpensesCents: cents(monthlyCompanyExpenses),
		EmployerContribCents: cents(employerContrib),
		GrossSalaryCents:     cents(gross),
		EmployeeContribCents: cents(employeeContrib),
		IncomeTaxCents:       cents(incomeTax),
		NetIncomeCents:       cents(net),
	}
	v.closeCompanyOver(stock)
	return v
}

// carryCompanyStock is where a balance parts company with every other figure
// here: the flows above it are summed across the months, while a balance opens
// where the first month opened and closes where the last one closed.
func (v *PersonalView) carryCompanyStock(m PersonalView, first bool) {
	if !m.ShowCompanyBalance {
		return
	}
	v.ShowCompanyBalance = true
	if first {
		v.CompanyOpeningCents = m.CompanyOpeningCents
	}
	v.CompanyClosingCents = m.CompanyClosingCents
	v.CompanyTargetCents = m.CompanyTargetCents
}

// closeCompanyOver settles the company balance from the rounded cent fields
// rather than from the floats they came from. The closing figure seeds the
// next month, so a half-cent here compounds down the year — and the rows on
// the page have to add up to it, because that is the first thing a reader
// checks.
func (v *PersonalView) closeCompanyOver(stock companyStock) {
	if !stock.Known {
		return
	}
	v.ShowCompanyBalance = true
	v.CompanyOpeningCents = stock.OpeningCents
	v.CompanyTargetCents = stock.TargetCents
	v.CompanyClosingCents = stock.OpeningCents + v.CompanyIncomeCents -
		v.CompanyExpensesCents - v.EmployerContribCents - v.GrossSalaryCents
}

func grossSalaryFor(d SalaryDecision, r Rules, availableForPayroll float64) (gross float64, minimumEnforced bool) {
	switch d.Mode {
	case SalaryNone:
		return 0, false
	case SalaryMinimum:
		return toCent(r.MinimumEUR), false
	case SalaryFixed:
		return toCent(d.FixedEUR), false
	}
	gross = toCent(r.Employer.grossAffordable(availableForPayroll))
	if gross < 0 {
		gross = 0
	}
	if r.MinimumEUR > 0 && gross < r.MinimumEUR {
		return toCent(r.MinimumEUR), true
	}
	return gross, false
}

func wholeRemainderAfter(employerContrib, availableForPayroll, gross float64) float64 {
	remainder := availableForPayroll - employerContrib
	if gross > 0 && remainder >= 0 {
		return remainder
	}
	return gross
}

func taxableAfter(gross, employeeContrib float64) float64 {
	if taxable := gross - employeeContrib; taxable > 0 {
		return taxable
	}
	return 0
}

func (p PersonalParams) breakdownMonths(monthlyIncomeEUR, monthlyCompanyExpensesEUR []float64, start yearMonth, opening companyStock) PersonalView {
	var result PersonalView
	var one PersonalView
	stock := opening
	for i, income := range monthlyIncomeEUR {
		var companyExpenses float64
		if i < len(monthlyCompanyExpensesEUR) {
			companyExpenses = monthlyCompanyExpensesEUR[i]
		}
		ym := start.addMonths(i)
		stock = p.targetStock(ym, stock)
		d := p.decide(ym, stock)
		mode := d.Mode
		m := p.breakdown(income, companyExpenses, 1, p.rulesFor(ym), d, stock)
		one = m
		switch mode {
		case SalaryMinimum:
			result.MonthsAtMinimum++
		case SalaryNone:
			result.MonthsWithoutSalary++
		case SalaryFixed:
			result.MonthsAtFixed++
		}
		if d.HeldForTarget {
			result.MonthsHeldForTarget++
			result.HeldForTarget = true
		}
		if d.TargetIdle {
			result.TargetIdle = true
		}
		if i == 0 {
			result.Mode = mode
		} else if result.Mode != mode {
			result.Mode = ""
		}
		if m.MinimumEnforced {
			result.MinimumEnforced = true
		}
		if m.NoLegislation {
			result.NoLegislation = true
		}
		if m.MinimumWageCents > result.MinimumWageCents {
			result.MinimumWageCents = m.MinimumWageCents
		}
		if m.FixedSalaryCents > result.FixedSalaryCents {
			result.FixedSalaryCents = m.FixedSalaryCents
		}
		result.CompanyIncomeCents += m.CompanyIncomeCents
		result.CompanyExpensesCents += m.CompanyExpensesCents
		result.EmployerContribCents += m.EmployerContribCents
		result.GrossSalaryCents += m.GrossSalaryCents
		result.EmployeeContribCents += m.EmployeeContribCents
		result.IncomeTaxCents += m.IncomeTaxCents
		result.NetIncomeCents += m.NetIncomeCents
		result.carryCompanyStock(m, i == 0)
		stock.OpeningCents = m.CompanyClosingCents
	}
	months := len(monthlyIncomeEUR)
	if months == 1 {
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
		out[0].Span = ""
	}
	return out
}

func describeSchedule(b Bands, minBase float64) string {
	if len(b) == 0 {
		return ""
	}
	return b.applied(b[len(b)-1].From+1, minBase)
}

func spanLabel(start yearMonth, from, to int) string {
	first := start.addMonths(from).Month.String()[:3]
	if from == to {
		return first
	}
	return first + "–" + start.addMonths(to).Month.String()[:3]
}
