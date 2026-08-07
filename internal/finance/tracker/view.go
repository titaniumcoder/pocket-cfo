package tracker

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// Tracker aggregates the Toggl and Holidays sources into the figures and
// renders the HTML. Already-tracked amounts (the "Tracked" row/rate) come
// straight from the Toggl detailed report, per project — unaffected by
// RateCents below. RateCents/RateCurrency are the primary source of the
// *projection* rate used for "Expected" (not-yet-tracked) work — a plain
// config value, not fetched from Toggl (see PocketCFO's Phase 3 plan: the
// primary use is predictions off a configured hourly rate). Toggl itself is
// an optional, config-toggled tracked-hours layer: a nil Toggl (rather than
// a bool flag) disables it entirely — same nil-means-"not configured"
// convention as Budget below — and every Toggl method degrades to an empty,
// error-free result for a nil receiver, so compute needs no separate
// tracking-disabled branch of its own. Invoiced (see invoiced.go) is that
// second layer: once a real invoice covers a linked client's period, its
// total replaces that client's Toggl-derived contribution to
// Tracked/(funding) income, keyed off the invoice recipient's resolved Toggl
// client ID (or UnscopedClientID) — a nil Invoiced map behaves exactly like
// "no invoices exist yet", same nil-means-"not configured" convention as
// Toggl/Budget.
type Tracker struct {
	Toggl        *Toggl
	Holidays     *Holidays
	Budget       *Budget
	HoursPerDay  float64
	Loc          *time.Location
	Personal     PersonalParams
	VacationDays int                    // annual paid-leave allowance, used in the year view
	RateCents    int                    // configured hourly rate used to project Expected work
	RateCurrency string                 // ISO code for RateCents, e.g. "EUR"; "" defaults to EUR (see CurrencySymbol)
	Invoiced     map[int]InvoicedClient // Toggl client ID (or UnscopedClientID) -> its invoicing state, see invoiced.go
}

type TrackedRow struct {
	Project     string
	Hours       string
	Rate        string
	AmountCents int
}

// InvoicedRow is one project's real invoiced income that became usable in
// the viewed period (see InvoicedClient.Usable) — shown separately
// from Tracked since it's realized income, not a Toggl-derived figure.
type InvoicedRow struct {
	Project     string
	AmountCents int
}

type HolidayView struct {
	Date string
	Name string
	// Current marks a holiday that falls within the viewed month — only
	// ever set in month view, since year view has no single "current"
	// month to highlight against.
	Current bool
}

type MonthOption struct {
	Num  int
	Name string
}

// Figures holds everything the dashboard renders. A non-empty *Err field means
// that section failed.
type Figures struct {
	Month    string
	Currency string // symbol, e.g. "€"

	// Nav/session-derived presentation fields — unlike every other field
	// on Figures, these are NOT set by compute(); the HTTP layer
	// (cmd/pocketcfo) fills them in from the session right before calling
	// RenderPage, same as index.html's own view struct does for the
	// invoicing dashboard. ShowInvoicingLink reflects whether this
	// session has access to the invoicing part at all — "users with
	// access to both get shared links, others land to wherever they have
	// access to" (see PocketCFO's plan §5.2).
	Login             string
	ReadOnly          bool
	ShowInvoicingLink bool

	// Navigation. Mode is "month" or "year"; the period selects and prev/next
	// arrows adapt accordingly.
	Mode         string
	Year         int
	MonthNum     int
	PrevURL      string
	NextURL      string
	PrevDisabled bool
	NextDisabled bool
	Years        []int
	Months       []MonthOption
	MonthViewURL string // target of the "Month" toggle
	YearViewURL  string // target of the "Year" toggle
	TodayURL     string // jumps to the actual current month, regardless of Mode/viewed period
	RefreshURL   string // current view + ?refresh=1 (clears the Toggl cache)
	LastUpdated  string // when the Toggl data was last fetched

	// MinimalMode/MinimalToggleURL back the minimal-budget toggle shown in
	// the Expenses panel — month view only; MinimalToggleURL is left empty
	// in year view (see fillYearNav). The flag itself is global (not tied
	// to this specific month) — see Budget.IsMinimal.
	MinimalMode      bool
	MinimalToggleURL string // current month view + ?minimal=toggle

	// Billable tracked work this month, one row per project+rate (includes
	// today) — excludes any project+month an issued invoice has already
	// superseded (see Tracker.Invoiced/invoiceSuppresses).
	Tracked    []TrackedRow
	TrackedErr string

	// Real invoiced income that became usable this period (see
	// InvoicedClient.Usable) — one row per project, folded into
	// TotalCents alongside Tracked/Expected.
	Invoiced      []InvoicedRow
	InvoicedCents int

	// Expected work still to come this month, at the most-used rate.
	ExpectedRange    string
	ExpectedHours    string
	ExpectedRate     string
	ExpectedCents    int
	ExpectedErr      string
	ExpectedNetHours string
	ExpectedNetCents int

	// Remaining paid leave (year view only). Days already taken are past workdays
	// with no billable time; the remaining allowance is deducted from expected.
	ShowVacation          bool
	VacationTotal         int
	VacationTaken         int
	VacationRemaining     int
	VacationHoursDeducted string
	VacationCentsDeducted int
	VacationErr           string

	// Projected month total = tracked + expected — the whole of the Income
	// panel's bottom line, rendered simply as "Income".
	TotalHours string
	TotalRate  string
	TotalCents int
	TotalErr   string

	// SpendableLabel/SpendableURL identify when this period's own Income
	// (TotalCents above) actually becomes available to spend — the viewed
	// period shifted forward two calendar months (the mirror of
	// FundingPersonal.FundingLabel/URL below; see spendRangeForMonth/Year) —
	// rendered inline next to "Income" as "(for September 2026)", a link
	// forward to the Expenses panel it will eventually fund.
	SpendableLabel string // e.g. "September 2026"
	SpendableURL   string

	// Public holidays for the whole viewed year, shown in a sidebar (not
	// filtered to the viewed month) — the ones falling in the viewed month
	// are marked via HolidayView.Current (month view only).
	Holidays    []HolidayView
	HolidaysErr string

	// Personal is the company-income → net-income cascade for the viewed
	// period itself (Bulgaria's employer-social/gross-salary/employee-social/
	// income-tax waterfall) — kept for the JSON API
	// (GET /api/net-income/...), which reports figures for the period the
	// caller actually requested. Not rendered on the dashboard itself
	// anymore — see FundingPersonal below for what the Expenses panel shows.
	Personal PersonalView

	// Expenses: private-kind categories from data/budget.json, read from the
	// local checkout. A flat monthly plan, not envelope budgeting and not
	// actual tracking — no logged entries, no target, no rollover. Company-kind
	// categories don't appear here — see FundingPersonal.CompanyGroups instead,
	// since they're deducted from Company income, not Net income.
	PrivateGroups          []CategoryGroupView
	PrivateTotalSpentCents int
	BudgetErr              string

	// FundingPersonal is the company-income → net-income cascade the
	// Expenses panel actually renders: computed for the period that funds
	// the viewed period's private expenses — the viewed period shifted back
	// two calendar months (money earned in month M is paid at the end of
	// M+1 and only becomes spendable from M+2), never the same period as
	// Personal above — see Tracker.fundingIncome.
	FundingPersonal PersonalView

	// The bottom line: FundingPersonal.NetIncomeCents − private Expenses for
	// the viewed period — see Tracker.compute. Company expenses for the
	// funding period are already accounted for inside
	// FundingPersonal.NetIncomeCents, so they aren't subtracted again here.
	ShowBalance  bool
	BalanceCents int
}

// ComputeMonth builds the figures for a single month (year + month).
func (t *Tracker) ComputeMonth(ctx context.Context, year int, month time.Month) Figures {
	now := time.Now().In(t.Loc)
	start := time.Date(year, month, 1, 0, 0, 0, 0, t.Loc)
	end := start.AddDate(0, 1, -1) // last day of the month
	result := t.compute(ctx, year, start, end, start.Format("January 2006"), 1, 0)
	result.fillMonthNav(now, start)
	return result
}

// ComputeYear builds the figures for a full calendar year.
func (t *Tracker) ComputeYear(ctx context.Context, year int) Figures {
	now := time.Now().In(t.Loc)
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
	end := start.AddDate(1, 0, -1) // 31 December
	result := t.compute(ctx, year, start, end, start.Format("2006"), 12, t.VacationDays)
	result.fillYearNav(now, start)
	return result
}

// compute builds the figures for the period [start, end] (inclusive) within the
// given year, independent of whether the period is a month or the whole year.
// All Toggl data comes from a single yearly fetch and is then filtered to the
// period; the caller fills in the navigation fields afterwards.
func (t *Tracker) compute(ctx context.Context, year int, start, end time.Time, label string, months, vacationDays int) Figures {
	now := time.Now().In(t.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc)

	result := Figures{Month: label, Currency: "€"}

	// today's tracked time only matters when today falls inside the period.
	isCurrentPeriod := !today.Before(start) && !today.After(end)

	projects, perr := t.Toggl.Projects(ctx)
	// One yearly fetch feeds tracked rows, the rate, today's status and the
	// billable-day set; filter it to the period in question.
	yd, terr := t.Toggl.Year(ctx, year)
	aggs := aggregatesInRange(yd, start, end, func(pid int, m time.Month) bool {
		return t.invoiceSuppresses(pid, yearMonth{year, m})
	})
	todayErr := terr // today's status shares the yearly fetch's fate
	todayTracked := isCurrentPeriod && terr == nil && yd.Days[today.Format("2006-01-02")]
	// Fetched for the whole year (not just the viewed period) so the
	// holidays sidebar can always show the full year — a strict superset,
	// so the workday/vacation calculations below (which only ever look at
	// dates within [start,end]) are unaffected.
	yearStart := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
	yearEnd := yearStart.AddDate(1, 0, -1)
	holidays, herr := t.Holidays.Fetch(ctx, yearStart, yearEnd)

	holidaySet := map[string]bool{}
	for _, hd := range holidays {
		holidaySet[hd.Date.Format("2006-01-02")] = true
	}

	result.computeVacation(vacationDays, today, start, herr, terr, holidaySet, yd)

	trackedHours, trackedCents, monthlyCompanyCents := result.computeTrackedRows(t, projects, aggs, yd, terr, perr, year, start, end)

	result.computeInvoicedRows(t, projects, year, start, end)

	rateCents, currency := t.RateCents, t.RateCurrency
	if currency != "" {
		result.Currency = CurrencySymbol(currency)
	}

	expectedNetHours, expectedNetCentsByMonth, expectedOK := result.computeExpected(t, year, start, end, today, todayTracked, herr, todayErr, holidaySet, rateCents)
	for m, cents := range expectedNetCentsByMonth {
		monthlyCompanyCents[m] += cents
	}

	result.computeTotal(trackedHours, trackedCents, expectedNetHours, expectedOK, rateCents)

	// Holidays: the whole year, with the ones in the viewed month marked
	// (month view only — year view has no single "current" month).
	if herr != nil {
		result.HolidaysErr = herr.Error()
	} else {
		for _, hd := range holidays {
			result.Holidays = append(result.Holidays, HolidayView{
				Date:    hd.Date.Format("Mon, 02 Jan"),
				Name:    hd.Name,
				Current: months == 1 && !hd.Date.Before(start) && !hd.Date.After(end),
			})
		}
	}

	bv := result.computeBudget(t, ctx, year, start, now, months)

	result.computePersonal(t, ctx, year, months, monthlyCompanyCents, bv)

	result.computeSpendable(months, year, start)

	result.computeFundingBalance(t, ctx, year, start, now, months, rateCents, bv)

	if at := t.Toggl.YearFetchedAt(year); !at.IsZero() {
		result.LastUpdated = at.In(t.Loc).Format("02 Jan 15:04")
	} else {
		result.LastUpdated = "—"
	}

	return result
}

// computeVacation fills in the remaining-paid-leave figures (year view only):
// the annual allowance minus the past workdays already spent without
// anything billable. The remaining days are later deducted from expected
// work by computeExpected.
func (f *Figures) computeVacation(vacationDays int, today, start time.Time, herr, terr error, holidaySet map[string]bool, yd *YearData) {
	if vacationDays <= 0 {
		return
	}
	f.VacationTotal = vacationDays
	ok := true
	if herr != nil {
		f.VacationErr = herr.Error()
		ok = false
	} else if terr != nil {
		f.VacationErr = terr.Error()
		ok = false
	} else if today.After(start) {
		// freeWorkdays only inspects days before today; the yearly billable-day
		// set covers them.
		f.VacationTaken = freeWorkdays(start, today, holidaySet, yd.Days)
	}
	if ok {
		if rem := vacationDays - f.VacationTaken; rem > 0 {
			f.VacationRemaining = rem
		}
	}
}

// computeTrackedRows fills in the Tracked rows (one per project+rate, most
// time first) and returns the running hours/cents totals plus each month's
// company income so far, for computeTotal/computePersonal to build on.
func (f *Figures) computeTrackedRows(t *Tracker, projects map[int]Project, aggs []Aggregate, yd *YearData, terr, perr error, year int, start, end time.Time) (trackedHours float64, trackedCents int, monthlyCompanyCents map[time.Month]int) {
	monthlyCompanyCents = map[time.Month]int{}
	if terr != nil {
		f.TrackedErr = terr.Error()
		return 0, 0, monthlyCompanyCents
	}
	if perr != nil {
		f.TrackedErr = perr.Error()
		return 0, 0, monthlyCompanyCents
	}
	for m := start.Month(); m <= end.Month(); m++ {
		ym := yearMonth{year, m}
		for _, a := range yd.Months[m] {
			if t.invoiceSuppresses(a.ProjectID, ym) {
				continue
			}
			monthlyCompanyCents[m] += a.AmountCents
		}
		monthlyCompanyCents[m] += t.invoicedCentsForMonth(ym)
	}
	sort.Slice(aggs, func(i, j int) bool { return aggs[i].Seconds > aggs[j].Seconds })
	for _, a := range aggs {
		name := projects[a.ProjectID].Name
		if name == "" {
			name = "(no project)"
		}
		hours := aggHours(a)
		f.Tracked = append(f.Tracked, TrackedRow{
			Project:     name,
			Hours:       formatHM(hours),
			Rate:        formatNum(float64(a.RateCents) / 100),
			AmountCents: a.AmountCents,
		})
		trackedHours += hours
		trackedCents += a.AmountCents
	}
	return trackedHours, trackedCents, monthlyCompanyCents
}

// computeInvoicedRows fills in the real invoiced income that became usable
// during the viewed period (see Tracker.Invoiced/InvoicedClient.Usable) —
// independent of the Toggl fetch above, since it comes from real invoice
// data, not a Toggl fetch.
//
// TODO(subtask 4): rewrite to one row per invoice via
// t.invoicedInvoicesForMonth, dropping the (now client-ID-keyed, not
// project-ID-keyed) projects[pid].Name lookup below — kept only to keep the
// build green after invoiced.go's client-keyed redesign.
func (f *Figures) computeInvoicedRows(t *Tracker, projects map[int]Project, year int, start, end time.Time) {
	for pid, ip := range t.Invoiced {
		var cents int
		for m := start.Month(); m <= end.Month(); m++ {
			for _, inv := range ip.Usable[yearMonth{year, m}] {
				cents += inv.Cents
			}
		}
		if cents == 0 {
			continue
		}
		name := projects[pid].Name
		if name == "" {
			name = "(no project)"
		}
		f.Invoiced = append(f.Invoiced, InvoicedRow{Project: name, AmountCents: cents})
		f.InvoicedCents += cents
	}
	sort.Slice(f.Invoiced, func(i, j int) bool { return f.Invoiced[i].AmountCents > f.Invoiced[j].AmountCents })
}

// computeExpected fills in the expected (not-yet-tracked) work remaining in
// the period, at the configured projection rate, and returns the net expected
// hours/per-month cents (with vacation already deducted) for the caller to
// fold into total company income.
func (f *Figures) computeExpected(t *Tracker, year int, start, end, today time.Time, todayTracked bool, herr, todayErr error, holidaySet map[string]bool, rateCents int) (expectedNetHours float64, expectedNetCentsByMonth map[time.Month]int, ok bool) {
	expectedCentsByMonth := map[time.Month]int{}
	expectedNetCentsByMonth = map[time.Month]int{}
	if herr != nil || todayErr != nil {
		f.ExpectedErr = firstErr(herr, todayErr)
		return 0, expectedNetCentsByMonth, false
	}

	remaining, todayIsWorkday := workdayInfo(start, end, today, holidaySet)
	days := remaining
	expStart := today.AddDate(0, 0, 1)
	if todayIsWorkday && !todayTracked {
		days++
		expStart = today
	}
	// For future months today is before the month; the expected work starts
	// at the month's first day rather than tomorrow.
	if expStart.Before(start) {
		expStart = start
	}
	expectedHours := float64(days) * t.HoursPerDay
	vacationHours := float64(f.VacationRemaining) * t.HoursPerDay
	expectedNetHours = expectedHours - vacationHours
	if expectedNetHours < 0 {
		expectedNetHours = 0
		vacationHours = expectedHours
	}
	vacationHoursLeft := vacationHours
	for m := start.Month(); m <= end.Month(); m++ {
		monthStart := time.Date(year, m, 1, 0, 0, 0, 0, t.Loc)
		monthEnd := monthStart.AddDate(0, 1, -1)
		if monthStart.Before(start) {
			monthStart = start
		}
		if monthEnd.After(end) {
			monthEnd = end
		}
		monthRemaining, monthTodayIsWorkday := workdayInfo(monthStart, monthEnd, today, holidaySet)
		monthDays := monthRemaining
		if monthTodayIsWorkday && !todayTracked {
			monthDays++
		}
		monthHours := float64(monthDays) * t.HoursPerDay
		monthVacationHours := math.Min(vacationHoursLeft, monthHours)
		vacationHoursLeft -= monthVacationHours
		expectedCentsByMonth[m] = round(monthHours * float64(rateCents))
		expectedNetCentsByMonth[m] = round((monthHours - monthVacationHours) * float64(rateCents))
	}

	f.ExpectedHours = formatCompactHours(expectedHours)
	f.ExpectedRate = formatNum(float64(rateCents) / 100)
	f.ExpectedCents = sumMonthCents(expectedCentsByMonth)
	f.ExpectedNetHours = formatCompactHours(expectedNetHours)
	f.ExpectedNetCents = sumMonthCents(expectedNetCentsByMonth)
	if f.VacationRemaining > 0 && vacationHours > 0 {
		f.ShowVacation = true
		f.VacationHoursDeducted = formatCompactHours(vacationHours)
		f.VacationCentsDeducted = f.ExpectedCents - f.ExpectedNetCents
	} else {
		f.ShowVacation = false
	}
	if days > 0 {
		f.ExpectedRange = expStart.Format("02.01.") + " - " + end.Format("02.01.06")
	} else {
		f.ExpectedRange = "—"
	}
	return expectedNetHours, expectedNetCentsByMonth, true
}

// computeTotal fills in Total = tracked + expected + invoiced. TotalHours/
// TotalRate stay hours-based (Tracked+Expected only) — invoiced income is a
// real lump sum with no meaningful hours figure of its own, by design (an
// invoice replaces predicted/tracked hours with a real total, not a real
// hours count).
func (f *Figures) computeTotal(trackedHours float64, trackedCents int, expectedNetHours float64, expectedOK bool, rateCents int) {
	if f.TrackedErr == "" && expectedOK {
		f.TotalHours = formatHM(trackedHours + expectedNetHours)
		f.TotalRate = formatNum(float64(rateCents) / 100)
		f.TotalCents = trackedCents + f.ExpectedNetCents + f.InvoicedCents
	} else {
		f.TotalErr = "unavailable"
	}
}

// computeBudget fetches Budget (private-kind Expenses, company-kind
// categories feeding the salary cascade, and Loans) — fetched once, ahead of
// the Personal income calc, since that calc needs company expenses. A nil
// Budget (not configured), or a fetch error, just leaves company expenses at
// zero and the Expenses/Debts sections empty rather than erroring the whole
// page — mirrors how the rest of compute degrades gracefully per section;
// company expenses silently defaulting to zero on a Budget error is an
// accepted edge case (an embedded static file essentially never fails at
// runtime).
func (f *Figures) computeBudget(t *Tracker, ctx context.Context, year int, start, now time.Time, months int) BudgetView {
	var bv BudgetView
	if t.Budget == nil {
		return bv
	}
	var berr error
	if months > 1 {
		bv, berr = t.Budget.ForYear(ctx, year, now)
	} else {
		f.MinimalMode = t.Budget.IsMinimal()
		bv, berr = t.Budget.ForMonth(ctx, year, start.Month(), now)
	}
	if berr != nil {
		f.BudgetErr = berr.Error()
	}
	return bv
}

// computePersonal fills in the company-income → net personal income
// (Bulgaria) cascade for the viewed period itself. Company (business)
// expenses are deducted first, before the employer-social/gross-salary/
// employee-social/income-tax cascade — see PersonalParams.breakdown — so
// they never show up as if they were personal salary spending.
func (f *Figures) computePersonal(t *Tracker, ctx context.Context, year, months int, monthlyCompanyCents map[time.Month]int, bv BudgetView) {
	if f.TotalErr != "" {
		f.Personal = PersonalView{Err: "company income unavailable"}
		return
	}
	if months > 1 {
		var monthlyCompanyExpenseCents map[time.Month]int
		if t.Budget != nil && f.BudgetErr == "" {
			if m, err := t.Budget.CompanyExpensesByMonth(ctx, year); err != nil {
				f.BudgetErr = err.Error()
			} else {
				monthlyCompanyExpenseCents = m
			}
		}
		start := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
		end := start.AddDate(1, 0, -1)
		f.Personal = t.Personal.breakdownMonths(
			monthlyIncomeEUR(start, end, monthlyCompanyCents),
			monthlyIncomeEUR(start, end, monthlyCompanyExpenseCents),
		)
	} else {
		f.Personal = t.Personal.breakdown(float64(f.TotalCents)/100, float64(bv.CompanyTotalSpentCents)/100, 1)
	}
	f.Personal.CompanyGroups = bv.CompanyGroups
}

// computeSpendable fills in SpendableLabel/URL: when this period's own Income
// (TotalCents) actually becomes spendable — the viewed period shifted forward
// two calendar months (the mirror of the funding shift in
// computeFundingBalance), rendered inline as "Income (for September 2026)".
// Pure calendar arithmetic on the viewed period, so it can't fail once Total
// itself is available, and independent of Budget/Personal — it's a property
// of Income alone.
func (f *Figures) computeSpendable(months, year int, start time.Time) {
	if f.TotalErr != "" {
		return
	}
	var spendStart, spendEnd yearMonth
	if months > 1 {
		spendStart, spendEnd = spendRangeForYear(year)
	} else {
		spendStart, spendEnd = spendRangeForMonth(year, start.Month())
	}
	f.SpendableLabel = rangeLabel(spendStart, spendEnd)
	f.SpendableURL = linkForRange(spendStart, spendEnd, spendStart.Year)
}

// computeFundingBalance fills in FundingPersonal (the company-income →
// net-income cascade actually rendered in the Expenses panel) and the bottom
// line ShowBalance/BalanceCents. Only the raw labor income (Tracked +
// Expected) is shifted back fundingShiftMonths calendar months, since salary
// earned in month M only becomes spendable from M+2 (see
// Tracker.fundingIncome) — applies uniformly to every viewed period,
// past/current/future alike. Company-kind expenses (bv.CompanyGroups/
// CompanyTotalSpentCents, fetched by computeBudget) are a real-time business
// fact, not subject to the payroll lag, so they stay tied to the VIEWED
// period — same as bv.Groups and computePersonal above — not the shifted
// funding period: a one-off cost like "Computer" dated 2026-09-01 shows/hides
// based on whichever month is actually being browsed, never postponed by the
// income shift.
func (f *Figures) computeFundingBalance(t *Tracker, ctx context.Context, year int, start, now time.Time, months, rateCents int, bv BudgetView) {
	var fundingStart, fundingEnd yearMonth
	if months > 1 {
		fundingStart, fundingEnd = fundingRangeForYear(year, now)
	} else {
		fundingStart, fundingEnd = fundingRangeForMonth(year, start.Month())
	}
	f.FundingPersonal = t.fundingIncome(ctx, fundingStart, fundingEnd, now, rateCents, float64(bv.CompanyTotalSpentCents)/100, bv.CompanyGroups)

	if t.Budget == nil {
		return
	}
	if f.BudgetErr == "" {
		f.PrivateGroups = bv.Groups
		f.PrivateTotalSpentCents = bv.TotalSpentCents
	}

	// The bottom line: funding income minus this period's private expenses.
	// Company expenses for the funding period are already deducted inside
	// FundingPersonal.NetIncomeCents, so they aren't subtracted again here.
	if f.BudgetErr == "" && f.FundingPersonal.Err == "" {
		f.ShowBalance = true
		f.BalanceCents = f.FundingPersonal.NetIncomeCents - f.PrivateTotalSpentCents
	}
}

// EvictMonth drops the cached Toggl and budget data for the given month (the
// Reload button in month view). Also evicts the funding period's Toggl
// cache — it can land in a different calendar year than the viewed month
// (see fundingRangeForMonth), which the viewed month's own eviction above
// wouldn't otherwise reach.
func (t *Tracker) EvictMonth(year int, month time.Month) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, t.Loc)
	t.Toggl.EvictRange(start, start.AddDate(0, 1, -1))
	fs, fe := fundingRangeForMonth(year, month)
	t.evictFundingRange(fs, fe)
	if t.Budget != nil {
		t.Budget.Evict()
	}
}

// EvictYear drops the cached Toggl and budget data for the given year (the
// Reload button in year view). Also evicts the funding range's Toggl cache
// — it can reach into the previous calendar year (see fundingRangeForYear),
// which the viewed year's own eviction above wouldn't otherwise reach.
func (t *Tracker) EvictYear(year int) {
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
	t.Toggl.EvictRange(start, start.AddDate(1, 0, -1))
	fs, fe := fundingRangeForYear(year, time.Now().In(t.Loc))
	t.evictFundingRange(fs, fe)
	if t.Budget != nil {
		t.Budget.Evict()
	}
}

// fillMonthNav populates navigation for month view: prev/next month links, the
// selected year/month, the dropdown options (months 1–12, years within the
// allowed range around now), and the view-toggle targets.
func (f *Figures) fillMonthNav(now, start time.Time) {
	prev := start.AddDate(0, -1, 0)
	next := start.AddDate(0, 1, 0)
	minYear, maxYear := navYearBounds(now)
	f.Mode = "month"
	f.Year = start.Year()
	f.MonthNum = int(start.Month())
	if prev.Year() < minYear {
		f.PrevDisabled = true
	} else {
		f.PrevURL = fmt.Sprintf("/%d/%d", prev.Year(), int(prev.Month()))
	}
	if next.Year() > maxYear {
		f.NextDisabled = true
	} else {
		f.NextURL = fmt.Sprintf("/%d/%d", next.Year(), int(next.Month()))
	}
	f.Years = navYears(now)
	for m := time.January; m <= time.December; m++ {
		f.Months = append(f.Months, MonthOption{Num: int(m), Name: m.String()})
	}
	f.MonthViewURL = fmt.Sprintf("/%d/%d", start.Year(), int(start.Month()))
	f.YearViewURL = fmt.Sprintf("/%d", start.Year())
	f.TodayURL = fmt.Sprintf("/%d/%d", now.Year(), int(now.Month()))
	f.RefreshURL = f.MonthViewURL + "?refresh=1"
	f.MinimalToggleURL = f.MonthViewURL + "?minimal=toggle"
}

// fillYearNav populates navigation for year view: prev/next year links, the
// selected year, the year dropdown, and the view-toggle
// targets. Switching to month view lands on the current month when viewing the
// current year, otherwise January.
func (f *Figures) fillYearNav(now, start time.Time) {
	minYear, maxYear := navYearBounds(now)
	f.Mode = "year"
	f.Year = start.Year()
	if start.Year() <= minYear {
		f.PrevDisabled = true
	} else {
		f.PrevURL = fmt.Sprintf("/%d", start.Year()-1)
	}
	if start.Year() >= maxYear {
		f.NextDisabled = true
	} else {
		f.NextURL = fmt.Sprintf("/%d", start.Year()+1)
	}
	f.Years = navYears(now)
	f.YearViewURL = fmt.Sprintf("/%d", start.Year())
	month := time.January
	if start.Year() == now.Year() {
		month = now.Month()
	}
	f.MonthViewURL = fmt.Sprintf("/%d/%d", start.Year(), int(month))
	f.TodayURL = fmt.Sprintf("/%d/%d", now.Year(), int(now.Month()))
	f.RefreshURL = f.YearViewURL + "?refresh=1"
}

// navYears returns the selectable years around the current year.
func navYears(now time.Time) []int {
	minYear, maxYear := navYearBounds(now)
	years := make([]int, 0, maxYear-minYear+1)
	for y := minYear; y <= maxYear; y++ {
		years = append(years, y)
	}
	return years
}

func navYearBounds(now time.Time) (int, int) {
	const yearRange = 2
	return now.Year() - yearRange, now.Year() + yearRange
}

// aggHours derives hours from the Toggl-calculated amount and rate so hours and
// money stay consistent; falls back to seconds when no rate is set.
func aggHours(a Aggregate) float64 {
	if a.RateCents > 0 {
		return float64(a.AmountCents) / float64(a.RateCents)
	}
	return float64(a.Seconds) / 3600
}

// aggregatesInRange merges the per-month aggregates of yd for every month the
// inclusive period [start, end] touches, re-summing per project + rate.
// Returns nil when yd is nil (a failed yearly fetch). start and end lie in
// the same year. suppress (see Tracker.invoiceSuppresses), if non-nil,
// drops a given month's aggregates for a project an issued invoice has
// already superseded — checked per source month, before merging, so a
// range straddling a project's invoice horizon still splits correctly
// instead of being suppressed (or not) as an all-or-nothing unit.
func aggregatesInRange(yd *YearData, start, end time.Time, suppress func(projectID int, m time.Month) bool) []Aggregate {
	if yd == nil {
		return nil
	}
	type key struct{ pid, rate int }
	acc := map[key]*Aggregate{}
	var order []key
	for m := start.Month(); m <= end.Month(); m++ {
		for _, a := range yd.Months[m] {
			if suppress != nil && suppress(a.ProjectID, m) {
				continue
			}
			k := key{a.ProjectID, a.RateCents}
			cur := acc[k]
			if cur == nil {
				cp := a
				acc[k] = &cp
				order = append(order, k)
				continue
			}
			cur.AmountCents += a.AmountCents
			cur.Seconds += a.Seconds
		}
	}
	out := make([]Aggregate, 0, len(order))
	for _, k := range order {
		out = append(out, *acc[k])
	}
	return out
}

func sumMonthCents(values map[time.Month]int) int {
	sum := 0
	for _, cents := range values {
		sum += cents
	}
	return sum
}

func monthlyIncomeEUR(start, end time.Time, monthlyCents map[time.Month]int) []float64 {
	income := make([]float64, 0, int(end.Month()-start.Month())+1)
	for m := start.Month(); m <= end.Month(); m++ {
		income = append(income, float64(monthlyCents[m])/100)
	}
	return income
}

func firstErr(errs ...error) string {
	for _, e := range errs {
		if e != nil {
			return e.Error()
		}
	}
	return ""
}

// workdayInfo counts workdays (Mon–Fri, not a holiday) strictly after today, and
// reports whether today itself is a workday.
func workdayInfo(start, end, today time.Time, holidays map[string]bool) (remaining int, todayIsWorkday bool) {
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		if holidays[d.Format("2006-01-02")] {
			continue
		}
		if d.After(today) {
			remaining++
		}
		if d.Equal(today) {
			todayIsWorkday = true
		}
	}
	return remaining, todayIsWorkday
}

// freeWorkdays counts workdays (Mon–Fri, not a public holiday) strictly before
// today that have no billable time logged — i.e. paid-leave days already taken.
func freeWorkdays(start, today time.Time, holidays, billable map[string]bool) int {
	count := 0
	for d := start; d.Before(today); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		key := d.Format("2006-01-02")
		if holidays[key] || billable[key] {
			continue
		}
		count++
	}
	return count
}
