package tracker

import (
	"context"
	"fmt"
	"time"
)

const estimateLagMonths = 2

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

type EstimatedInvoice struct {
	WorkedMonth yearMonth
	Cents       int
}

// fundingIncome is the cascade for the months a page covers, funded the way
// the company is actually paid: an invoice is income in the month after it
// was due, and a worked month whose invoice does not exist yet contributes
// the estimate for it — income two months after the work, where the invoice
// will land once the hours are in.
func (t *Tracker) fundingIncome(ctx context.Context, months []yearMonth, now time.Time, companyExpensesEUR float64, companyGroups []CategoryGroupView) (PersonalView, map[yearMonth][]EstimatedInvoice) {
	if len(months) == 0 {
		return PersonalView{}, nil
	}

	estimates, err := t.estimatedInvoices(ctx, months, now)
	if err != nil {
		pv := t.breakdownFunding(t.invoiceOnlyIncome(months), companyExpensesEUR, companyGroups, months[0])
		pv.Err = err.Error()
		return pv, nil
	}

	incomeEUR := make([]float64, len(months))
	for i, m := range months {
		incomeEUR[i] = float64(t.invoicedCentsForMonth(m)+estimateCents(estimates, m)) / 100
	}
	return t.breakdownFunding(incomeEUR, companyExpensesEUR, companyGroups, months[0]), estimates
}

func (t *Tracker) breakdownFunding(incomeEUR []float64, companyExpensesEUR float64, companyGroups []CategoryGroupView, start yearMonth) PersonalView {
	perMonthExpenseEUR := companyExpensesEUR / float64(len(incomeEUR))
	expensesEUR := make([]float64, len(incomeEUR))
	for i := range expensesEUR {
		expensesEUR[i] = perMonthExpenseEUR
	}

	pv := t.Personal.breakdownMonths(incomeEUR, expensesEUR, start)
	pv.CompanyGroups = companyGroups
	return pv
}

func (t *Tracker) invoiceOnlyIncome(months []yearMonth) []float64 {
	incomeEUR := make([]float64, len(months))
	for i, m := range months {
		incomeEUR[i] = float64(t.invoicedCentsForMonth(m)) / 100
	}
	return incomeEUR
}

// estimatedInvoices prices, for every income month the page covers, the
// worked month two earlier — the invoice that does not exist yet. A month
// whose work the issued invoices already cover contributes nothing.
func (t *Tracker) estimatedInvoices(ctx context.Context, incomeMonths []yearMonth, now time.Time) (map[yearMonth][]EstimatedInvoice, error) {
	if t.Toggl == nil {
		return nil, nil
	}
	worked := make([]yearMonth, len(incomeMonths))
	for i, im := range incomeMonths {
		worked[i] = im.addMonths(-estimateLagMonths)
	}
	years := distinctYears(worked)

	togglCtx, cancelToggl := waitBudget(ctx)
	defer cancelToggl()

	projects, err := t.hours().Projects(togglCtx)
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

	out := map[yearMonth][]EstimatedInvoice{}
	for i, im := range incomeMonths {
		wm := worked[i]
		cents := t.monthEstimateCents(wm, projects, ydByYear[wm.Year], holidaysByYear[wm.Year], today)
		if cents != 0 {
			out[im] = append(out[im], EstimatedInvoice{WorkedMonth: wm, Cents: cents})
		}
	}
	return out, nil
}

func (t *Tracker) fetchYearDataByYear(ctx context.Context, years []int) (map[int]*YearData, error) {
	ydByYear := map[int]*YearData{}
	for _, y := range years {
		yd, err := t.hours().Year(ctx, y)
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

// monthEstimateCents prices a worked month as the invoice that does not
// exist yet: the tracked hours no issued invoice already reaches, plus the
// workdays still unbilled after that. Coverage is per client — written-down
// tracking_until where an invoice names a day, the end of the issue month
// where none does — and the workdays stop being projected only when every
// billable client that tracked work in the month is already invoiced
// through its end: with one client billed whole and another not, the
// untracked days belong to whoever is still unbilled.
func (t *Tracker) monthEstimateCents(ym yearMonth, projects map[int]Project, yd *YearData, holidays map[string]bool, today time.Time) int {
	if yd == nil {
		return 0
	}
	monthStart := time.Date(ym.Year, ym.Month, 1, 0, 0, 0, 0, t.Loc)
	monthEnd := monthStart.AddDate(0, 1, -1)

	var trackedCents int
	var projected time.Time
	fullyBilled, sawBillable := true, false
	for _, a := range yd.Months[ym.Month] {
		clientID := projects[a.ProjectID].ClientID
		if t.ignoresClient(clientID) {
			continue
		}
		sawBillable = true
		f := t.Invoiced.frontierFor(clientID)
		if !f.IsZero() && !f.Before(monthEnd) {
			continue
		}
		fullyBilled = false
		if f.After(projected) {
			projected = f
		}
		if !f.IsZero() && !f.Before(monthStart) {
			continue
		}
		trackedCents += a.AmountCents
	}

	if sawBillable && fullyBilled {
		return trackedCents
	}

	rateCents := t.RateCents
	if frontierInsideMonth(projected, monthStart, monthEnd) {
		// The monthly aggregates cannot price individual days, so the
		// unbilled remainder after the frontier is priced by day: every
		// workday it holds is work the next invoice will carry, whether
		// its hours are tracked yet or not.
		days := countWorkdays(projected.AddDate(0, 0, 1), monthEnd, holidays)
		return trackedCents + round(float64(days)*t.HoursPerDay*float64(rateCents))
	}

	remaining, todayIsWorkday := workdayInfo(monthStart, monthEnd, today, holidays)
	days := remaining
	todayTracked := yd.Days[today.Format("2006-01-02")]
	if todayIsWorkday && !todayTracked {
		days++
	}
	return trackedCents + round(float64(days)*t.HoursPerDay*float64(rateCents))
}

func frontierInsideMonth(frontier, monthStart, monthEnd time.Time) bool {
	return !frontier.IsZero() && !frontier.Before(monthStart) && frontier.Before(monthEnd)
}

func (t *Tracker) ignoresClient(clientID int) bool {
	return t.IgnoredClientIDs[clientID]
}

func countWorkdays(start, end time.Time, holidays map[string]bool) int {
	days := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		if holidays[d.Format("2006-01-02")] {
			continue
		}
		days++
	}
	return days
}

func estimateCents(estimates map[yearMonth][]EstimatedInvoice, ym yearMonth) int {
	var total int
	for _, e := range estimates[ym] {
		total += e.Cents
	}
	return total
}
