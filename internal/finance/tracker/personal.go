package tracker

// PersonalParams turns company income into net personal income for a Bulgarian
// one-person company (EOOD) paying the owner a salary. Rates are fractions
// (0.10 = 10%); money is in EUR.
//
// Company expenses come off first (see breakdown's companyExpensesEUR), since
// that money never becomes salary; only what's left becomes salary cost. Both
// social contributions are capped at the monthly maximum insurable income.
// Private spending is deducted from Net income elsewhere, not here.
type PersonalParams struct {
	EmployerRate        float64 // employer social + health contributions, e.g. 0.1892
	EmployeeRate        float64 // employee social + health contributions, e.g. 0.1378
	MaxInsurableMonthly float64 // monthly maximum insurable income (EUR); 0 = no cap
	IncomeTaxRate       float64 // personal income tax, e.g. 0.10
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

	CompanyGroups []CategoryGroupView

	// Which period's Company income this cascade came from. Set only on
	// Figures.FundingPersonal, where it differs from the viewed period; blank
	// on Figures.Personal, which is already the period the caller asked about.
	FundingLabel string // e.g. "November 2025"
	FundingURL   string // jumps to that funding period

	// Formatted rate percentages for the labels (e.g. "18.92").
	EmployerPct  string
	EmployeePct  string
	IncomeTaxPct string
}

// breakdown computes the waterfall over `months` months. Salary is smoothed
// evenly so the monthly social-contribution cap applies correctly, then the
// per-month figures are scaled back up for display.
func (p PersonalParams) breakdown(totalIncomeEUR, companyExpensesEUR float64, months int) PersonalView {
	result := PersonalView{
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
// monthly caps for partial years and uneven income. A short or nil
// monthlyCompanyExpensesEUR treats missing months as zero.
func (p PersonalParams) breakdownMonths(monthlyIncomeEUR, monthlyCompanyExpensesEUR []float64) PersonalView {
	result := PersonalView{
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
		result.CompanyIncomeCents += m.CompanyIncomeCents
		result.CompanyExpensesCents += m.CompanyExpensesCents
		result.EmployerContribCents += m.EmployerContribCents
		result.GrossSalaryCents += m.GrossSalaryCents
		result.EmployeeContribCents += m.EmployeeContribCents
		result.IncomeTaxCents += m.IncomeTaxCents
		result.NetIncomeCents += m.NetIncomeCents
	}
	return result
}
