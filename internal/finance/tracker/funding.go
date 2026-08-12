package tracker

import (
	"context"
	"fmt"
	"time"
)

// fundingShiftMonths is the gap between earning company income and being able
// to spend it: salary earned in M is paid end of M+1, so it funds expenses from
// M+2. Applies uniformly to every viewed period, never conditionally on now.
const fundingShiftMonths = -2

// yearMonth identifies a month across year boundaries. Unlike compute's
// [start,end], which stays within one calendar year, a funding or spendable
// period can cross into the previous or next one.
type yearMonth struct {
	Year  int
	Month time.Month
}

// addMonths returns the month n months away, rolling over the year.
func (ym yearMonth) addMonths(n int) yearMonth {
	t := time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
	return yearMonth{t.Year(), t.Month()}
}

// String renders the month as e.g. "May 2026".
func (ym yearMonth) String() string {
	return time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, time.UTC).Format("January 2006")
}

// ordinal orders yearMonths chronologically, for invoiced.go's comparisons.
func (ym yearMonth) ordinal() int { return ym.Year*12 + int(ym.Month) }

// configForm is the month as config.json writes it, for the places that exist
// to be compared against the file rather than read as prose.
func (ym yearMonth) configForm() string {
	return fmt.Sprintf("%04d-%02d", ym.Year, int(ym.Month))
}

// monthsBetween returns every month from start to end inclusive. Callers
// guarantee start <= end.
func monthsBetween(start, end yearMonth) []yearMonth {
	const safetyCap = 36 // no viewed period in this app spans anywhere near this many months
	months := make([]yearMonth, 0, 12)
	for m, i := start, 0; i < safetyCap; m, i = m.addMonths(1), i+1 {
		months = append(months, m)
		if m == end {
			break
		}
	}
	return months
}

// fundingRangeForMonth returns the month that funds the viewed month's
// expenses: viewed minus two. May land in the previous year.
func fundingRangeForMonth(year int, month time.Month) (start, end yearMonth) {
	shifted := yearMonth{year, month}.addMonths(fundingShiftMonths)
	return shifted, shifted
}

// fundingRangeForYear shifts back the exact start..December range ForYear uses
// for private expenses, so the two can never drift apart. The end never crosses
// a year boundary; the start does when the expense range begins in January or
// February.
func fundingRangeForYear(year int, now time.Time, floor yearMonth) (start, end yearMonth) {
	expenseStart := yearMonth{year, privateExpenseStartMonth(year, now, floor)}
	expenseEnd := yearMonth{year, time.December}
	return expenseStart.addMonths(fundingShiftMonths), expenseEnd.addMonths(fundingShiftMonths)
}

// spendRangeForMonth mirrors fundingRangeForMonth, shifted forward instead of
// back: viewing November means usable from January.
func spendRangeForMonth(year int, month time.Month) (start, end yearMonth) {
	shifted := yearMonth{year, month}.addMonths(-fundingShiftMonths)
	return shifted, shifted
}

// spendRangeForYear shifts the full Jan-Dec income year forward, so unlike the
// expense range it always crosses into the next year at the end:
// March(year)..February(year+1).
func spendRangeForYear(year int) (start, end yearMonth) {
	start = yearMonth{year, time.January}.addMonths(-fundingShiftMonths)
	end = yearMonth{year, time.December}.addMonths(-fundingShiftMonths)
	return start, end
}

// rangeLabel renders a range as "November 2025" or "November 2025 – October 2026".
func rangeLabel(start, end yearMonth) string {
	if start == end {
		return start.String()
	}
	return start.String() + " – " + end.String()
}

// linkForRange links a single month to "/{year}/{month}" and a multi-month
// range to the year view of majorityYear. The shift only crosses a year
// boundary at one end, so the caller passes whichever year the range mostly is.
func linkForRange(start, end yearMonth, majorityYear int) string {
	if start == end {
		return fmt.Sprintf("/%d/%d", start.Year, int(start.Month))
	}
	return fmt.Sprintf("/%d", majorityYear)
}

// fundingIncome computes the cascade rendered in the Expenses panel. Only labor
// income is shifted back to [start,end]; companyExpensesEUR/companyGroups are
// the caller's VIEWED-period figures, unshifted, since company expenses are a
// real-time business fact rather than subject to the payroll lag.
func (t *Tracker) fundingIncome(ctx context.Context, start, end yearMonth, now time.Time, rateCents int, companyExpensesEUR float64, companyGroups []CategoryGroupView) PersonalView {
	label := rangeLabel(start, end)
	url := linkForRange(start, end, end.Year)
	months := monthsBetween(start, end)
	incomeEUR, err := t.fundingLaborIncomeEUR(ctx, months, now, rateCents)
	if err != nil {
		return PersonalView{Err: err.Error(), FundingLabel: label, FundingURL: url}
	}

	// Spread evenly only so breakdownMonths' per-month cap has a same-length
	// slice to pair against. The total deducted is exact however it is split,
	// and company expenses are rarely large enough to be cap-sensitive.
	perMonthExpenseEUR := companyExpensesEUR / float64(len(incomeEUR))
	expensesEUR := make([]float64, len(incomeEUR))
	for i := range expensesEUR {
		expensesEUR[i] = perMonthExpenseEUR
	}

	// The floor is indexed by the month the salary is PAID, not the month the
	// income arrived — start..end is shifted back by the payroll lag, so
	// undoing that shift is what lands on the viewed month. Getting this wrong
	// means the minimum wage takes effect two months after employment does,
	// silently.
	pv := t.Personal.breakdownMonths(incomeEUR, expensesEUR, start.addMonths(-fundingShiftMonths))
	pv.CompanyGroups = companyGroups
	pv.FundingLabel = label
	pv.FundingURL = url
	return pv
}

// fundingLaborIncomeEUR sums each funding month's labor income, before any
// company-expense deduction. Generalizes compute's per-viewed-month loop to an
// arbitrary, possibly year-crossing list of months.
func (t *Tracker) fundingLaborIncomeEUR(ctx context.Context, months []yearMonth, now time.Time, rateCents int) ([]float64, error) {
	if len(months) == 0 {
		return nil, nil
	}
	years := distinctYears(months)

	// Same short budget as the viewed period's reads (see waitBudget); on the
	// raw request context these would sit out the wait the Income panel just
	// avoided.
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

// fetchYearDataByYear fetches a report per distinct year the funding months
// touch; a period spanning a year boundary needs both.
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

// fetchHolidaysByYear fetches each distinct year's public holidays and
// indexes them by "YYYY-MM-DD" for workdayInfo's lookup.
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

// monthLaborIncome computes one funding month's labor income: tracked cents
// plus projected cents for what's left of the month. No vacation deduction —
// that allowance belongs to the viewed year, not an unrelated funding period.
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
	// ym is a funding month, i.e. viewed - 2, so the month it funds is ym + 2.
	// UsableCents is already keyed by the real calendar month it becomes usable
	// in (due date + 1), which is that viewed month — looking it up at ym would
	// double-shift an already-correct due-date-derived timing.
	trackedCents += t.invoicedCentsForMonth(ym.addMonths(-fundingShiftMonths))

	// workdayInfo already returns 0 for a wholly past month and every workday
	// for a wholly future one, so no special-casing is needed here.
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

// evictFundingRange evicts the Toggl cache for [start,end] (yearMonth,
// inclusive) — the funding period may touch a calendar year the viewed
// month/year's own eviction doesn't reach (see fundingRangeForMonth/Year).
func (t *Tracker) evictFundingRange(start, end yearMonth) {
	s := time.Date(start.Year, start.Month, 1, 0, 0, 0, 0, t.Loc)
	e := time.Date(end.Year, end.Month, 1, 0, 0, 0, 0, t.Loc).AddDate(0, 1, -1)
	t.Toggl.EvictRange(s, e)
}
