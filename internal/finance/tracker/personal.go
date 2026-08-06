package tracker

// PersonalParams are the parameters for turning company income into net personal
// income for a Bulgarian one-person company (EOOD) paying the owner a salary.
// All rates are fractions (0.10 = 10%); all money is in EUR (Bulgaria adopted the
// euro on 2026-01-01).
//
// Company (business) expenses are deducted from company income before any of
// this runs — see breakdown's companyExpensesEUR parameter — since that money
// never becomes personal salary at all; only what's left becomes salary cost
// (gross salary + employer social contributions). Gross salary and employer
// social contributions are deductible, and both social contributions are
// capped at the monthly maximum insurable income. Private (personal) spending
// is tracked separately (see Budgeting) and deducted from Net income, not here.
type PersonalParams struct {
	EmployerRate        float64 // employer social + health contributions, e.g. 0.1892
	EmployeeRate        float64 // employee social + health contributions, e.g. 0.1378
	MaxInsurableMonthly float64 // monthly maximum insurable income (EUR); 0 = no cap
	IncomeTaxRate       float64 // personal income tax, e.g. 0.10
}

// PersonalView is the rendered company→personal waterfall. Money is in cents.
// CompanyGroups is filled in by Tracker.compute (not breakdown/breakdownMonths,
// which only deal with the numeric cascade) for display purposes — the
// company-kind budget.json groups, shown under Company income.
type PersonalView struct {
	Err string

	CompanyIncomeCents   int
	CompanyExpensesCents int
	EmployerContribCents int
	GrossSalaryCents     int
	EmployeeContribCents int
	IncomeTaxCents       int
	NetIncomeCents       int

	CompanyGroups []CategoryGroupView

	// FundingLabel/FundingURL identify which period's Company income this
	// cascade was actually computed from — set only on Figures.FundingPersonal
	// (the viewed period shifted back two calendar months; see
	// Tracker.fundingIncome), left blank on Figures.Personal (the viewed
	// period's own, API-only cascade, which needs no such label since it's
	// already the period the caller asked about). Filled in by
	// Tracker.fundingIncome, not breakdown/breakdownMonths (which only
	// handle the numeric cascade), same as CompanyGroups above.
	FundingLabel string // e.g. "November 2025"
	FundingURL   string // jumps to that funding period

	// Formatted rate percentages for the labels (e.g. "18.92").
	EmployerPct  string
	EmployeePct  string
	IncomeTaxPct string
}

// breakdown computes the waterfall over `months` months given the total company
// income (EUR) for the whole period, minus companyExpensesEUR (business costs
// for the same period — deducted first, since that money never becomes salary
// at all). Salary is smoothed evenly across the months so the monthly
// social-contribution cap applies correctly; the per-month figures are then
// scaled back up by `months` for display.
func (p PersonalParams) breakdown(totalIncomeEUR, companyExpensesEUR float64, months int) PersonalView {
	v := PersonalView{
		EmployerPct:  formatNum(p.EmployerRate * 100),
		EmployeePct:  formatNum(p.EmployeeRate * 100),
		IncomeTaxPct: formatNum(p.IncomeTaxRate * 100),
	}
	if months <= 0 {
		months = 1
	}

	monthlyRawIncome := totalIncomeEUR / float64(months)
	monthlyCompanyExpenses := companyExpensesEUR / float64(months)
	monthlyIncome := monthlyRawIncome - monthlyCompanyExpenses

	// Solve gross salary G so that G + employerContrib(G) == monthlyIncome, where
	// the employer contribution is capped at the monthly maximum insurable income.
	gross := monthlyIncome / (1 + p.EmployerRate)
	if p.MaxInsurableMonthly > 0 && gross > p.MaxInsurableMonthly {
		// Employer contribution is flat (capped), so the rest is salary.
		gross = monthlyIncome - p.EmployerRate*p.MaxInsurableMonthly
	}
	if gross < 0 {
		gross = 0
	}

	base := gross // insurable base, capped at the monthly maximum
	if p.MaxInsurableMonthly > 0 && base > p.MaxInsurableMonthly {
		base = p.MaxInsurableMonthly
	}
	employerContrib := p.EmployerRate * base
	employeeContrib := p.EmployeeRate * base

	taxable := gross - employeeContrib
	if taxable < 0 {
		taxable = 0
	}
	incomeTax := p.IncomeTaxRate * taxable
	net := gross - employeeContrib - incomeTax

	m := float64(months)
	cents := func(x float64) int { return round(x * 100 * m) }
	v.CompanyIncomeCents = cents(monthlyRawIncome)
	v.CompanyExpensesCents = cents(monthlyCompanyExpenses)
	v.EmployerContribCents = cents(employerContrib)
	v.GrossSalaryCents = cents(gross)
	v.EmployeeContribCents = cents(employeeContrib)
	v.IncomeTaxCents = cents(incomeTax)
	v.NetIncomeCents = cents(net)
	return v
}

// breakdownMonths computes the personal-income waterfall month by month and
// sums the results. This preserves monthly social-insurance caps for partial
// years and uneven income. monthlyCompanyExpensesEUR must be the same length
// as monthlyIncomeEUR (index i's company expenses apply to index i's income);
// a shorter or nil slice treats missing months as zero company expenses.
func (p PersonalParams) breakdownMonths(monthlyIncomeEUR, monthlyCompanyExpensesEUR []float64) PersonalView {
	v := PersonalView{
		EmployerPct:  formatNum(p.EmployerRate * 100),
		EmployeePct:  formatNum(p.EmployeeRate * 100),
		IncomeTaxPct: formatNum(p.IncomeTaxRate * 100),
	}
	for i, income := range monthlyIncomeEUR {
		var companyExpenses float64
		if i < len(monthlyCompanyExpensesEUR) {
			companyExpenses = monthlyCompanyExpensesEUR[i]
		}
		m := p.breakdown(income, companyExpenses, 1)
		v.CompanyIncomeCents += m.CompanyIncomeCents
		v.CompanyExpensesCents += m.CompanyExpensesCents
		v.EmployerContribCents += m.EmployerContribCents
		v.GrossSalaryCents += m.GrossSalaryCents
		v.EmployeeContribCents += m.EmployeeContribCents
		v.IncomeTaxCents += m.IncomeTaxCents
		v.NetIncomeCents += m.NetIncomeCents
	}
	return v
}
