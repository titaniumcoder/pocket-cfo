package tracker

import (
	"context"
	"fmt"
	"time"
)

const fundingShiftMonths = -2

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

func monthsBetween(start, end yearMonth) []yearMonth {
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

func fundingRangeForMonth(year int, month time.Month) (start, end yearMonth) {
	shifted := yearMonth{year, month}.addMonths(fundingShiftMonths)
	return shifted, shifted
}

func fundingRangeForYear(year int, now time.Time, floor yearMonth) (start, end yearMonth) {
	expenseStart := yearMonth{year, privateExpenseStartMonth(year, now, floor)}
	expenseEnd := yearMonth{year, time.December}
	return expenseStart.addMonths(fundingShiftMonths), expenseEnd.addMonths(fundingShiftMonths)
}

func spendRangeForMonth(year int, month time.Month) (start, end yearMonth) {
	shifted := yearMonth{year, month}.addMonths(-fundingShiftMonths)
	return shifted, shifted
}

func spendRangeForYear(year int) (start, end yearMonth) {
	start = yearMonth{year, time.January}.addMonths(-fundingShiftMonths)
	end = yearMonth{year, time.December}.addMonths(-fundingShiftMonths)
	return start, end
}

func rangeLabel(start, end yearMonth) string {
	if start == end {
		return start.String()
	}
	return start.String() + " – " + end.String()
}

func linkForRange(start, end yearMonth, majorityYear int) string {
	if start == end {
		return fmt.Sprintf("/%d/%d", start.Year, int(start.Month))
	}
	return fmt.Sprintf("/%d", majorityYear)
}

func (t *Tracker) fundingIncome(ctx context.Context, start, end yearMonth, now time.Time, rateCents int, companyExpensesEUR float64, companyGroups []CategoryGroupView, opening companyStock, dividends Dividends) PersonalView {
	label := rangeLabel(start, end)
	url := linkForRange(start, end, end.Year)
	months := monthsBetween(start, end)
	incomeEUR, err := t.fundingLaborIncomeEUR(ctx, months, now, rateCents)
	if err != nil {
		return PersonalView{Err: err.Error(), FundingLabel: label, FundingURL: url}
	}

	perMonthExpenseEUR := companyExpensesEUR / float64(len(incomeEUR))
	expensesEUR := make([]float64, len(incomeEUR))
	for i := range expensesEUR {
		expensesEUR[i] = perMonthExpenseEUR
	}

	pv := t.Personal.withDividends(dividends).breakdownMonths(incomeEUR, expensesEUR, start.addMonths(-fundingShiftMonths), opening)
	pv.CompanyGroups = companyGroups
	pv.FundingLabel = label
	pv.FundingURL = url
	return pv
}

func (t *Tracker) fundingLaborIncomeEUR(ctx context.Context, months []yearMonth, now time.Time, rateCents int) ([]float64, error) {
	if len(months) == 0 {
		return nil, nil
	}
	years := distinctYears(months)

	togglCtx, cancelToggl := waitBudget(ctx)
	defer cancelToggl()

	projects, err := t.Toggl.Projects(togglCtx)
	if err != nil {
		return nil, fmt.Errorf("funding: toggl projects: %w", err)
	}
	ydByYear, err := t.fetchYearDataByYear(togglCtx, years)
	if err != nil {
		return nil, err
	}
	holidaysByYear, err := t.fetchHolidaysByYear(ctx, years)
	if err != nil {
		return nil, err
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc)

	incomeEUR := make([]float64, len(months))
	for i, ym := range months {
		incomeEUR[i] = t.monthLaborIncome(ym, projects, ydByYear[ym.Year], holidaysByYear[ym.Year], today, rateCents)
	}

	return incomeEUR, nil
}

func (t *Tracker) fetchYearDataByYear(ctx context.Context, years []int) (map[int]*YearData, error) {
	ydByYear := map[int]*YearData{}
	for _, y := range years {
		yd, err := t.Toggl.Year(ctx, y)
		if err != nil {
			return nil, fmt.Errorf("funding: toggl %d: %w", y, err)
		}
		ydByYear[y] = yd
	}
	return ydByYear, nil
}

func (t *Tracker) fetchHolidaysByYear(ctx context.Context, years []int) (map[int]map[string]bool, error) {
	holidaysByYear := map[int]map[string]bool{}
	for _, y := range years {
		yearStart := time.Date(y, time.January, 1, 0, 0, 0, 0, t.Loc)
		yearEnd := yearStart.AddDate(1, 0, -1)
		hds, err := t.Holidays.Fetch(ctx, yearStart, yearEnd)
		if err != nil {
			return nil, fmt.Errorf("funding: holidays %d: %w", y, err)
		}
		set := map[string]bool{}
		for _, hd := range hds {
			set[hd.Date.Format("2006-01-02")] = true
		}
		holidaysByYear[y] = set
	}
	return holidaysByYear, nil
}

func (t *Tracker) monthLaborIncome(ym yearMonth, projects map[int]Project, yd *YearData, holidays map[string]bool, today time.Time, rateCents int) float64 {
	monthStart := time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, t.Loc)
	monthEnd := monthStart.AddDate(0, 1, -1)

	var trackedCents int
	for _, a := range yd.Months[ym.Month] {
		if t.invoiceSuppresses(projects[a.ProjectID].ClientID, ym) {
			continue
		}
		trackedCents += a.AmountCents
	}
	trackedCents += t.invoicedCentsForMonth(ym.addMonths(-fundingShiftMonths))

	remaining, todayIsWorkday := workdayInfo(monthStart, monthEnd, today, holidays)
	days := remaining
	todayTracked := yd.Days[today.Format("2006-01-02")]
	if todayIsWorkday && !todayTracked {
		days++
	}
	expectedCents := round(float64(days) * t.HoursPerDay * float64(rateCents))

	return float64(trackedCents+expectedCents) / 100
}

func distinctYears(months []yearMonth) []int {
	seen := map[int]bool{}
	var years []int
	for _, ym := range months {
		if !seen[ym.Year] {
			seen[ym.Year] = true
			years = append(years, ym.Year)
		}
	}
	return years
}

func (t *Tracker) evictFundingRange(start, end yearMonth) {
	s := time.Date(start.Year, start.Month, 1, 0, 0, 0, 0, t.Loc)
	e := time.Date(end.Year, end.Month, 1, 0, 0, 0, 0, t.Loc).AddDate(0, 1, -1)
	t.Toggl.EvictRange(s, e)
}
