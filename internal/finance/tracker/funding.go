package tracker

import (
	"fmt"
	"time"
)

type yearMonth struct {
	Year  int
	Month time.Month
}

func (ym yearMonth) addMonths(n int) yearMonth {
	t := time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
	return yearMonth{t.Year(), t.Month()}
}

func (ym yearMonth) String() string {
	return time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, time.UTC).Format("January 2006")
}

func (ym yearMonth) ordinal() int { return ym.Year*12 + int(ym.Month) }

func (ym yearMonth) configForm() string {
	return fmt.Sprintf("%04d-%02d", ym.Year, int(ym.Month))
}

// ConfigForm is configForm for the agent-facing read surface, which reports
// the dated rules in the same spelling config.json states them.
func (ym yearMonth) ConfigForm() string { return ym.configForm() }

func yearMonths(start, end yearMonth) []yearMonth {
	const safetyCap = 36
	months := make([]yearMonth, 0, 12)
	for m, i := start, 0; i < safetyCap; m, i = m.addMonths(1), i+1 {
		months = append(months, m)
		if m == end {
			break
		}
	}
	return months
}

// fundingIncome is the cascade for the months a page covers, funded the way
// the company is actually paid: an invoice is income in the month after it
// was due, and nothing else is. The company's planned expenses for the same
// months are what the salary has to leave behind.
func (t *Tracker) fundingIncome(months []yearMonth, companyExpensesEUR float64, companyGroups []CategoryGroupView) PersonalView {
	if len(months) == 0 {
		return PersonalView{}
	}
	incomeEUR := make([]float64, len(months))
	for i, m := range months {
		incomeEUR[i] = float64(t.invoicedCentsForMonth(m)) / 100
	}
	perMonthExpenseEUR := companyExpensesEUR / float64(len(months))
	expensesEUR := make([]float64, len(months))
	for i := range expensesEUR {
		expensesEUR[i] = perMonthExpenseEUR
	}

	pv := t.Personal.breakdownMonths(incomeEUR, expensesEUR, months[0])
	pv.CompanyGroups = companyGroups
	return pv
}
