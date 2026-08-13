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

func (p PersonalParams) breakdown(totalIncomeEUR, companyExpensesEUR float64, months int, r Rules, d SalaryDecision) PersonalView {
	if months <= 0 {
		months = 1
	}
	mode := d.Mode
	monthlyRawIncome := totalIncomeEUR / float64(months)
	monthlyCompanyExpenses := companyExpensesEUR / float64(months)
	availableForPayroll := toCent(monthlyRawIncome - monthlyCompanyExpenses)

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
	return PersonalView{
		NoLegislation:        r.nothingInForce(),
		Mode:                 mode,
		MinimumEnforced:      minimumEnforced,
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

func (p PersonalParams) breakdownMonths(monthlyIncomeEUR, monthlyCompanyExpensesEUR []float64, start yearMonth) PersonalView {
	var result PersonalView
	var one PersonalView
	for i, income := range monthlyIncomeEUR {
		var companyExpenses float64
		if i < len(monthlyCompanyExpensesEUR) {
			companyExpenses = monthlyCompanyExpensesEUR[i]
		}
		ym := start.addMonths(i)
		d := p.Salary.decisionFor(ym)
		mode := d.Mode
		m := p.breakdown(income, companyExpenses, 1, p.rulesFor(ym), d)
		one = m
		switch mode {
		case SalaryMinimum:
			result.MonthsAtMinimum++
		case SalaryNone:
			result.MonthsWithoutSalary++
		case SalaryFixed:
			result.MonthsAtFixed++
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
