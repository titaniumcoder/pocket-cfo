package tracker

import (
	"context"
	"fmt"
	"time"
)

// fundingShiftMonths is the fixed calendar-month gap between when company
// income is earned and when it becomes available to spend: salary earned in
// month M is paid out at the end of month M+1, so it only funds expenses
// from month M+2 onward. Applies uniformly to every viewed period — past,
// current or future — never conditionally on "now".
const fundingShiftMonths = -2

// yearMonth identifies one calendar month unambiguously across year
// boundaries. Unlike Tracker.compute's [start,end] (always within a single
// calendar year), a funding or spendable period can cross into the previous
// or next year, so it needs its own year, not just a time.Month.
type yearMonth struct {
	Year  int
	Month time.Month
}

// addMonths returns the month n calendar months away (n may be negative),
// rolling over the year as needed.
func (ym yearMonth) addMonths(n int) yearMonth {
	t := time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
	return yearMonth{t.Year(), t.Month()}
}

// String renders the month as e.g. "May 2026", for display labels.
func (ym yearMonth) String() string {
	return time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, time.UTC).Format("January 2006")
}

// ordinal totally orders yearMonth values chronologically (higher = later),
// for the "on or before" comparisons invoiced.go's invoice horizon needs.
func (ym yearMonth) ordinal() int { return ym.Year*12 + int(ym.Month) }

// monthsBetween returns every calendar month from start to end inclusive, in
// chronological order. Callers guarantee start <= end.
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

// fundingRangeForMonth returns the single shifted month that actually funds
// the given viewed month's expenses (month view) — the viewed month minus
// two calendar months. May land in the previous calendar year (e.g. viewing
// January -> November of the year before).
func fundingRangeForMonth(year int, month time.Month) (start, end yearMonth) {
	shifted := yearMonth{year, month}.addMonths(fundingShiftMonths)
	return shifted, shifted
}

// fundingRangeForYear returns the shifted month range that funds the given
// viewed year's expenses (year view) — the exact same start..December range
// Budget.ForYear uses for private expenses (see privateExpenseStartMonth),
// shifted back fundingShiftMonths months, so the two can never drift apart.
// The end (December - 2 = October) never crosses a year boundary; the start
// does whenever the expense range itself starts in January or February
// (only possible when year == now.Year()), landing in November or December
// of the previous year respectively.
func fundingRangeForYear(year int, now time.Time) (start, end yearMonth) {
	expenseStart := yearMonth{year, privateExpenseStartMonth(year, now)}
	expenseEnd := yearMonth{year, time.December}
	return expenseStart.addMonths(fundingShiftMonths), expenseEnd.addMonths(fundingShiftMonths)
}

// spendRangeForMonth returns the single month a viewed income month becomes
// spendable in — the mirror of fundingRangeForMonth, shifted forward instead
// of back. Viewing November -> usable from January of the next year.
func spendRangeForMonth(year int, month time.Month) (start, end yearMonth) {
	shifted := yearMonth{year, month}.addMonths(-fundingShiftMonths)
	return shifted, shifted
}

// spendRangeForYear returns the range a viewed year's Personal income
// becomes spendable in. Personal income in year view always spans the full
// viewed year (Jan-Dec — unlike private expenses' "remaining months" range),
// so shifting it forward always crosses into the next calendar year at the
// END: Jan+2..Dec+2 = March(year)..February(year+1).
func spendRangeForYear(year int) (start, end yearMonth) {
	start = yearMonth{year, time.January}.addMonths(-fundingShiftMonths)
	end = yearMonth{year, time.December}.addMonths(-fundingShiftMonths)
	return start, end
}

// rangeLabel renders a yearMonth range as bare text — "November 2025" for a
// single month, "November 2025 – October 2026" for a range. Used for both
// PersonalView.FundingLabel (inside "Company income (from ...)") and
// Figures.SpendableLabel (inside "Income (for ...)").
func rangeLabel(start, end yearMonth) string {
	if start == end {
		return start.String()
	}
	return start.String() + " – " + end.String()
}

// linkForRange returns the navigation URL for a (possibly multi-month)
// yearMonth range: a single month links straight to "/{year}/{month}"; a
// multi-month range (always a year-view figure) links to the Year View of
// majorityYear, since the shift only ever crosses a year boundary at one
// end (the start for funding, the end for spendable) — the caller passes
// whichever endpoint's year the range mostly belongs to.
func linkForRange(start, end yearMonth, majorityYear int) string {
	if start == end {
		return fmt.Sprintf("/%d/%d", start.Year, int(start.Month))
	}
	return fmt.Sprintf("/%d", majorityYear)
}

// fundingIncome computes the full company-income → net-income cascade
// (Figures.FundingPersonal) rendered in the Expenses panel. Only the raw
// labor income (Tracked + Expected) is shifted back to [start,end] (the
// viewed period minus fundingShiftMonths calendar months) — company-kind
// expenses are a real-time business fact, not subject to the payroll lag
// that motivates the shift in the first place (see fundingShiftMonths), so
// companyExpensesEUR/companyGroups are the caller's already-computed VIEWED
// period figures (Tracker.compute's bv.CompanyTotalSpentCents/CompanyGroups)
// — unshifted, exactly as they were before this feature existed, so e.g. a
// one-off cost dated 2026-09-01 shows/hides based on whichever month is
// actually being browsed, never postponed by the income shift. rateCents is
// the configured projection rate (Tracker.RateCents), reused as-is since
// it's a plain config value anchored to no particular period.
func (t *Tracker) fundingIncome(ctx context.Context, start, end yearMonth, now time.Time, rateCents int, companyExpensesEUR float64, companyGroups []CategoryGroupView) PersonalView {
	label := rangeLabel(start, end)
	url := linkForRange(start, end, end.Year)
	months := monthsBetween(start, end)
	incomeEUR, err := t.fundingLaborIncomeEUR(ctx, months, now, rateCents)
	if err != nil {
		return PersonalView{Err: err.Error(), FundingLabel: label, FundingURL: url}
	}

	// Spread the viewed period's total company expense evenly across the
	// funding months purely so PersonalParams.breakdownMonths' per-month
	// insurable cap has a same-length slice to pair each month's (real,
	// varying) labor income against — the total deducted is exact
	// regardless of how it's split across slots, and company expenses are
	// rarely large enough to be cap-sensitive themselves.
	perMonthExpenseEUR := companyExpensesEUR / float64(len(incomeEUR))
	expensesEUR := make([]float64, len(incomeEUR))
	for i := range expensesEUR {
		expensesEUR[i] = perMonthExpenseEUR
	}

	pv := t.Personal.breakdownMonths(incomeEUR, expensesEUR)
	pv.CompanyGroups = companyGroups
	pv.FundingLabel = label
	pv.FundingURL = url
	return pv
}

// fundingLaborIncomeEUR sums, per funding month, Toggl-tracked cents plus
// projected "expected" cents for whatever's left of that month relative to
// real now (same workdayInfo-driven formula Tracker.compute uses, minus the
// vacation-day deduction, which is tied to the *viewed* year's annual
// allowance and doesn't apply to an unrelated funding period) — the raw
// labor income the funding period earns, before any company-expense
// deduction (see fundingIncome). Generalized from Tracker.compute's
// per-viewed-month loop to an arbitrary, possibly year-boundary-crossing
// list of calendar months, independent of whichever period is currently
// being viewed.
func (t *Tracker) fundingLaborIncomeEUR(ctx context.Context, months []yearMonth, now time.Time, rateCents int) ([]float64, error) {
	if len(months) == 0 {
		return nil, nil
	}
	years := distinctYears(months)

	// The funding period's Toggl reads get the same short budget as the
	// viewed period's (see waitBudget). Left on the raw request context they
	// would sit out the whole fetch here instead, which is exactly the wait
	// the Income panel just avoided.
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

// fetchYearDataByYear fetches the Toggl yearly report for each distinct year
// the funding months touch — a funding period spanning a year boundary (see
// fundingRangeForMonth/Year) needs both years' data.
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

// monthLaborIncome computes one funding month's raw labor income: Toggl-
// tracked cents plus projected "expected" cents for whatever's left of the
// month relative to today (same workdayInfo-driven formula Tracker.compute
// uses, minus the vacation-day deduction, which is tied to the *viewed*
// year's annual allowance and doesn't apply to an unrelated funding period).
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
	// ym is a funding month, reached via fundingRangeForMonth(viewed) =
	// viewed.addMonths(fundingShiftMonths) — i.e. ym = viewed - 2. The
	// viewed month it funds is therefore ym.addMonths(-fundingShiftMonths) =
	// ym + 2. An invoice's UsableCents is already keyed by the real
	// calendar month it becomes usable in (due date + 1, see invoiced.go) —
	// that's the VIEWED month, not ym — so look it up at ym+2, not at ym
	// itself. Looking it up at ym directly double-shifts an already-correct
	// due-date-derived timing by another two months.
	trackedCents += t.invoicedCentsForMonth(ym.addMonths(-fundingShiftMonths))

	// workdayInfo naturally returns remaining=0 for a month wholly in
	// the past (nothing in [monthStart,monthEnd] is after today) and
	// counts every workday for a month wholly in the future — no
	// special-casing needed, same as compute's own per-month loop.
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
