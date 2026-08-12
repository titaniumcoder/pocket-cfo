package tracker

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

// Tracker aggregates the Toggl and Holidays sources into the figures.
//
// Tracked amounts come from Toggl per project and ignore RateCents, which is
// only the projection rate for Expected (not-yet-tracked) work.
//
// Toggl, Budget, Accounts and Invoiced are all optional layers, disabled by a
// nil rather than a flag: every method degrades to an empty, error-free result
// for a nil receiver, so compute needs no "not configured" branches.
type Tracker struct {
	Toggl        *Toggl
	Holidays     *Holidays
	Budget       *Budget
	Accounts     *Accounts
	Actuals      *Actuals
	HoursPerDay  float64
	Loc          *time.Location
	Personal     PersonalParams
	VacationDays int                    // annual paid-leave allowance, year view only
	RateCents    int                    // configured hourly rate, projects Expected work
	RateCurrency string                 // ISO code for RateCents; "" defaults to EUR (see CurrencySymbol)
	Invoiced     map[int]InvoicedClient // Toggl client ID (or UnscopedClientID) -> invoicing state, see invoiced.go
}

type TrackedRow struct {
	Project     string
	Hours       string
	Rate        string
	AmountCents int
}

// InvoicedRow is one issued invoice's real income that became usable in the
// viewed period (see InvoicedClient.Usable). Number is deliberately the bare
// invoice number, never the client name or title, so the row is safe to show
// without invoicing rights; URL is filled in by cmd/pocketcfo only for a
// session that has them, and the template falls back to plain text.
type InvoicedRow struct {
	Number      string
	AmountCents int
	URL         string
}

type HolidayView struct {
	Date    string
	Name    string
	Current bool // falls in the viewed month; month view only
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

	// Session-derived, and the only fields NOT set by compute() — cmd/pocketcfo
	// fills them from the session just before RenderPage. ShowInfoLink mirrors
	// s.authorized(sess), /info's own gate, so a readonly session never sees it.
	Login             string
	ReadOnly          bool
	ShowInvoicingLink bool
	ShowInfoLink      bool

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
	NavMonth     int    // the month the "Month" toggle lands on; a real month even in year view
	MonthViewURL string // target of the "Month" toggle
	YearViewURL  string // target of the "Year" toggle
	TodayURL     string // jumps to the actual current month, regardless of Mode/viewed period
	RefreshURL   string // current view + ?refresh=1 (clears the Toggl cache)
	LastUpdated  string // when the Toggl data was last fetched

	// MinimalMode/MinimalToggleURL back the minimal-budget toggle shown in
	// the Expenses panel. Month view only; the flag itself is global rather
	// than per-month (see Budget.IsMinimal).
	MinimalMode      bool
	MinimalToggleURL string // current month view + ?minimal=toggle

	// The three states the Toggl layer can be in, beyond "fine". TogglPending:
	// nothing cached yet but a fetch is running, so the page renders a loading
	// Income section and a meta refresh. TogglStaleNote: real figures, out of
	// date because a refresh failed. TrackedErr (below): nothing is coming.
	TogglPending   bool
	TogglStaleNote string

	// One row per project+rate, excluding any project+month an issued invoice
	// has superseded (see Tracker.Invoiced/invoiceSuppresses).
	Tracked    []TrackedRow
	TrackedErr string

	// Invoiced income that became usable this period (see InvoicedClient.Usable).
	Invoiced      []InvoicedRow
	InvoicedCents int

	// Work still to come, at the most-used rate. ShowExpected drops the whole
	// section once the period is past rather than render a row of zeroes; the
	// figures are already 0 there, so it is presentation, not arithmetic.
	ShowExpected     bool
	ExpectedRange    string
	ExpectedHours    string
	ExpectedRate     string
	ExpectedCents    int
	ExpectedErr      string
	ExpectedNetHours string
	ExpectedNetCents int

	// Remaining paid leave (year view only). Days taken are past workdays with
	// no billable time; the remainder is deducted from expected.
	ShowVacation          bool
	VacationTotal         int
	VacationTaken         int
	VacationRemaining     int
	VacationHoursDeducted string
	VacationCentsDeducted int
	VacationErr           string

	// tracked + expected: the Income panel's bottom line.
	TotalHours string
	TotalRate  string
	TotalCents int
	TotalErr   string

	// When this period's hours-based income becomes spendable: the viewed
	// period shifted forward two months (see spendRangeForMonth/Year), rendered
	// as "(for September 2026)". Empty for a period with no hours-based income
	// — the label describes hours, not the Invoiced rows, which carry their own
	// identity.
	SpendableLabel string
	SpendableURL   string

	// The whole viewed year, not filtered to the month; see HolidayView.Current.
	Holidays    []HolidayView
	HolidaysErr string

	// The Bulgaria company-income to net-income cascade for the viewed period.
	// Not rendered — FundingPersonal below is what the Expenses panel shows.
	Personal PersonalView

	// private-kind categories from budget.json. Company-kind ones live in
	// FundingPersonal.CompanyGroups instead: they come off Company income, not
	// Net income.
	PrivateGroups            []CategoryGroupView
	PrivateTotalPlannedCents int
	BudgetErr                string

	// Recorded spending, shown beside the plan and folded into nothing. False
	// ShowActuals renders byte-identically to a build without this layer.
	ShowActuals           bool
	PrivateActualCents    int
	CompanyActualCents    int
	PrivateUnmatchedCents int
	CompanyUnmatchedCents int
	Mistimed              []MistimedRow
	ActualsErr            string
	SpendingDetailURL     string // filled by cmd/pocketcfo, admin sessions only

	// ShowSpendingLink is the menu entry, which exists in year view too —
	// unlike SpendingDetailURL, which is deliberately empty there because no
	// single month's transactions explain a year's figures.
	ShowSpendingLink bool

	// The same cascade for the period that funds the viewed one: shifted back
	// two months, since money earned in M is paid end of M+1 and spendable from
	// M+2. Never the same period as Personal above. See Tracker.fundingIncome.
	FundingPersonal PersonalView

	// FundingPersonal.NetIncomeCents minus private expenses. Company expenses
	// are already inside NetIncomeCents, so they aren't subtracted twice.
	ShowBalance  bool
	BalanceCents int

	// The real bank balance from accounts.json — month view only, and only from
	// the month its snapshot opens (see computeAccountBalances). The panel reads
	// opening + income - expenses = Balance. No company-side equivalent exists:
	// business money is spent down rather than accumulated, so there is nothing
	// to carry. AccountsStaleNote nags when the snapshot is old, since the
	// roll-forward keeps producing confident figures off drifting data.
	ShowOpeningBalance  bool
	OpeningBalanceCents int
	OpeningBalanceLabel string
	AvailableCents      int
	PrivateAccounts     []AccountRow
	AccountsStaleNote   string

	AccountsErr string
}

// Header adapts the session-derived fields into internal/webui's own view. The
// dashboard is always reachable from itself, hence ShowFinance: true.
func (f Figures) Header() webui.Header {
	return webui.Header{
		Login:         f.Login,
		Active:        webui.PageFinance,
		ShowFinance:   true,
		ShowSpending:  f.ShowSpendingLink,
		ShowInvoicing: f.ShowInvoicingLink,
		ShowInfo:      f.ShowInfoLink,
		Period:        webui.Period{Year: f.Year, Month: f.NavMonth, YearView: f.Mode == "year"},
	}
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

	// Toggl gets a short slice of the request's budget: Tracker.Warm keeps the
	// data current, and a page's job is to use whatever is ready.
	togglCtx, cancelToggl := waitBudget(ctx)
	defer cancelToggl()

	projects, perr := t.Toggl.Projects(togglCtx)
	// One yearly fetch feeds tracked rows, the rate, today's status and the
	// billable-day set; filter it to the period in question.
	yd, terr := t.Toggl.Year(togglCtx, year)
	result.TogglPending = terr != nil && t.Toggl.YearPending(year)
	aggs := aggregatesInRange(yd, start, end)
	todayErr := terr // today's status shares the yearly fetch's fate
	todayTracked := isCurrentPeriod && terr == nil && yd.Days[today.Format("2006-01-02")]
	// Whole year, so the sidebar can show it all. A superset of [start,end], so
	// the workday/vacation calculations below are unaffected.
	yearStart := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
	yearEnd := yearStart.AddDate(1, 0, -1)
	holidays, herr := t.Holidays.Fetch(ctx, yearStart, yearEnd)

	holidaySet := map[string]bool{}
	for _, hd := range holidays {
		holidaySet[hd.Date.Format("2006-01-02")] = true
	}

	result.computeVacation(vacationDays, today, start, herr, terr, holidaySet, yd)

	trackedHours, trackedCents, monthlyCompanyCents := result.computeTrackedRows(t, projects, aggs, yd, terr, perr, year, start, end)

	result.computeInvoicedRows(t, year, start, end)

	rateCents, currency := t.RateCents, t.RateCurrency
	if currency != "" {
		result.Currency = CurrencySymbol(currency)
	}

	expectedNetHours, expectedNetCentsByMonth, expectedOK := result.computeExpected(t, year, start, end, today, todayTracked, herr, todayErr, holidaySet, rateCents)
	for m, cents := range expectedNetCentsByMonth {
		monthlyCompanyCents[m] += cents
	}
	// A period that has fully elapsed has no work still to come.
	result.ShowExpected = !today.After(end)

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

	result.computeActuals(t, ctx, year, start, now, months, &bv)

	result.computePersonal(t, ctx, year, months, yearMonth{year, start.Month()}, monthlyCompanyCents, bv)

	result.computeSpendable(months, year, start, trackedHours > 0 || expectedNetHours > 0)

	result.computeFundingBalance(t, ctx, year, start, now, months, rateCents, bv)

	if at, stale := t.Toggl.YearStatus(year); !at.IsZero() {
		result.LastUpdated = at.In(t.Loc).Format("02 Jan 15:04")
		if stale {
			result.TogglStaleNote = "Toggl didn't answer — tracked hours are the last ones fetched, on " + result.LastUpdated + "."
		}
	} else {
		result.LastUpdated = "—"
	}

	return result
}

// computeVacation fills in remaining paid leave (year view only): the annual
// allowance minus past workdays with nothing billable. computeExpected later
// deducts the remainder from expected work.
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

// computeTrackedRows fills in the Tracked rows, most time first, and returns
// the totals plus each month's company income for computeTotal/computePersonal.
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
		for _, a := range yd.Months[m] {
			monthlyCompanyCents[m] += a.AmountCents
		}
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

// computeInvoicedRows fills in invoiced income that became usable this period
// (see InvoicedClient.Usable), one row per invoice. Independent of the Toggl
// fetch. URL is left for the HTTP layer, which sets it only for sessions with
// invoicing rights.
func (f *Figures) computeInvoicedRows(t *Tracker, year int, start, end time.Time) {
	for m := start.Month(); m <= end.Month(); m++ {
		for _, inv := range t.invoicedInvoicesForMonth(yearMonth{year, m}) {
			f.Invoiced = append(f.Invoiced, InvoicedRow{Number: inv.Number, AmountCents: inv.Cents})
			f.InvoicedCents += inv.Cents
		}
	}
	sort.Slice(f.Invoiced, func(i, j int) bool { return f.Invoiced[i].Number < f.Invoiced[j].Number })
}

// computeExpected fills in not-yet-tracked work remaining in the period at the
// projection rate, returning net hours and per-month cents with vacation
// already deducted.
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

// computeTotal fills in tracked + expected. Invoiced money is deliberately
// absent: it belongs to the Rolling budget panel, which answers a different
// question, and counting it here would show the same euros twice on one page.
func (f *Figures) computeTotal(trackedHours float64, trackedCents int, expectedNetHours float64, expectedOK bool, rateCents int) {
	if f.TrackedErr == "" && expectedOK {
		f.TotalHours = formatHM(trackedHours + expectedNetHours)
		f.TotalRate = formatNum(float64(rateCents) / 100)
		f.TotalCents = trackedCents + f.ExpectedNetCents
	} else {
		f.TotalErr = "unavailable"
	}
}

// computeBudget fetches Budget once, ahead of the Personal income calc, which
// needs company expenses. A nil Budget or a fetch error leaves company expenses
// at zero and the sections empty rather than erroring the page — the silent
// zero is an accepted edge case, since a local file essentially never fails.
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

// computePersonal fills in the Bulgaria salary cascade for the viewed period.
// Company expenses come off first, before employer-social/gross/employee-social
// /tax (see PersonalParams.breakdown), so they never look like salary spending.
func (f *Figures) computePersonal(t *Tracker, ctx context.Context, year, months int, viewed yearMonth, monthlyCompanyCents map[time.Month]int, bv BudgetView) {
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
			yearMonth{year, time.January},
		)
	} else {
		f.Personal = t.Personal.breakdown(float64(f.TotalCents)/100, float64(bv.CompanyTotalPlannedCents)/100, 1,
			t.Personal.rulesFor(viewed))
	}
	f.Personal.CompanyGroups = bv.CompanyGroups
}

// computeSpendable fills in SpendableLabel/URL: the viewed period shifted
// forward two months, mirroring the funding shift in computeFundingBalance.
// Pure calendar arithmetic, so it can't fail once Total is available. Unset
// when hasHours is false — a purely invoiced period has no hours story, and
// the invoices carry their own identity.
func (f *Figures) computeSpendable(months, year int, start time.Time, hasHours bool) {
	if f.TotalErr != "" || !hasHours {
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

// computeFundingBalance fills in FundingPersonal and the bottom line.
//
// Only labor income is shifted back fundingShiftMonths, since salary earned in
// M is spendable from M+2. Company expenses are a real-time business fact, not
// subject to the payroll lag, so they stay tied to the VIEWED period: a one-off
// cost dated 2026-09-01 shows and hides with the month being browsed, never
// postponed by the income shift.
func (f *Figures) computeFundingBalance(t *Tracker, ctx context.Context, year int, start, now time.Time, months, rateCents int, bv BudgetView) {
	var fundingStart, fundingEnd yearMonth
	if months > 1 {
		fundingStart, fundingEnd = fundingRangeForYear(year, now)
	} else {
		fundingStart, fundingEnd = fundingRangeForMonth(year, start.Month())
	}
	f.FundingPersonal = t.fundingIncome(ctx, fundingStart, fundingEnd, now, rateCents, float64(bv.CompanyTotalPlannedCents)/100, bv.CompanyGroups)

	if t.Budget == nil {
		return
	}
	if f.BudgetErr == "" {
		f.PrivateGroups = bv.Groups
		f.PrivateTotalPlannedCents = bv.TotalPlannedCents
	}

	// Before the bottom line below, which builds on the opening balance.
	if months == 1 {
		f.computeAccountBalances(t, ctx, yearMonth{year, start.Month()}, now, rateCents)
	}

	// Company expenses are already inside NetIncomeCents, not subtracted twice.
	if f.BudgetErr == "" && f.FundingPersonal.Err == "" {
		f.ShowBalance = true
		f.BalanceCents = f.OpeningBalanceCents + f.FundingPersonal.NetIncomeCents - f.PrivateTotalPlannedCents
	}
}

// computeAccountBalances layers the real bank balance onto the viewed month.
//
// A snapshot OPENS the month after it was read: a figure read on 31 July is
// August's opening balance, not July's. From there it rolls forward a month at
// a time, each month closing with opening + net income - private expenses, so a
// loss-making month genuinely eats into it. Earlier months ignore it —
// back-projecting a balance would be fiction.
//
// Month view only: a year's "opening balance" would have to pick an anchor
// month and would quietly mean something different from the monthly figure.
func (f *Figures) computeAccountBalances(t *Tracker, ctx context.Context, viewed yearMonth, now time.Time, rateCents int) {
	if t.Accounts == nil || f.BudgetErr != "" || f.FundingPersonal.Err != "" {
		return
	}
	snap, ok := t.Accounts.Snapshot(ctx)
	if !ok || viewed.ordinal() < snap.OpensMonth.ordinal() {
		return
	}
	opening, err := t.privateOpeningCents(ctx, snap, viewed, now, rateCents)
	if err != nil {
		f.AccountsErr = err.Error()
		return
	}
	f.ShowOpeningBalance = true
	f.OpeningBalanceCents = opening
	f.AvailableCents = opening + f.FundingPersonal.NetIncomeCents
	f.OpeningBalanceLabel = openingBalanceLabel(snap, viewed)
	// Only in the snapshot's own month, where the opening figure IS those
	// balances. Once carried it is derived, and listing the original 4 200
	// under an opening balance of -70 reads as a contradiction.
	if viewed == snap.OpensMonth {
		f.PrivateAccounts = snap.AccountRow
	}
	if days, stale := snap.StaleDays(now); stale {
		f.AccountsStaleNote = fmt.Sprintf(
			"Last read %d days ago (%s). Everything below it is extrapolated from that one figure — go check your bank and update accounts.json.",
			days, snap.LatestAsOf.Format("2 January 2006"))
	}
}

// openingBalanceLabel says where the figure came from: the snapshot itself
// in its own month, or how far it has been carried since.
func openingBalanceLabel(snap AccountSnapshot, viewed yearMonth) string {
	if viewed == snap.OpensMonth {
		return "as read end of " + snap.OpensMonth.addMonths(-1).String()
	}
	return "carried from " + snap.OpensMonth.String()
}

// EvictMonth backs the Reload button in month view. It also evicts the funding
// period, which can land in a different calendar year (fundingRangeForMonth)
// and so isn't covered by the viewed month's own eviction.
func (t *Tracker) EvictMonth(year int, month time.Month) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, t.Loc)
	t.Toggl.EvictRange(start, start.AddDate(0, 1, -1))
	fs, fe := fundingRangeForMonth(year, month)
	t.evictFundingRange(fs, fe)
	if t.Budget != nil {
		t.Budget.Evict()
	}
	t.Accounts.Evict()
	t.Actuals.Evict()
}

// EvictYear backs the Reload button in year view. It also evicts the funding
// range, which reaches into the previous calendar year (fundingRangeForYear).
func (t *Tracker) EvictYear(year int) {
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
	t.Toggl.EvictRange(start, start.AddDate(1, 0, -1))
	fs, fe := fundingRangeForYear(year, time.Now().In(t.Loc))
	t.evictFundingRange(fs, fe)
	if t.Budget != nil {
		t.Budget.Evict()
	}
	t.Accounts.Evict()
	t.Actuals.Evict()
}

// MonthNav is the month stepper — arrows, selects, bounds — shared by the
// dashboard and the spending page. Both step through the same months against
// the same limits; only the addresses they step to differ.
type MonthNav struct {
	Year         int
	MonthNum     int
	Months       []MonthOption
	Years        []int
	PrevURL      string
	NextURL      string
	PrevDisabled bool
	NextDisabled bool
	TodayURL     string
}

func monthURL(year int, month time.Month) string { return fmt.Sprintf("/%d/%d", year, int(month)) }

func spendingURL(year int, month time.Month) string { return monthURL(year, month) + "/spending" }

func monthNav(now, start time.Time, url func(int, time.Month) string) MonthNav {
	prev, next := start.AddDate(0, -1, 0), start.AddDate(0, 1, 0)
	minYear, maxYear := navYearBounds(now)
	nav := MonthNav{
		Year:     start.Year(),
		MonthNum: int(start.Month()),
		Years:    navYears(now),
		TodayURL: url(now.Year(), now.Month()),
	}
	for m := time.January; m <= time.December; m++ {
		nav.Months = append(nav.Months, MonthOption{Num: int(m), Name: m.String()})
	}
	if prev.Year() < minYear {
		nav.PrevDisabled = true
	} else {
		nav.PrevURL = url(prev.Year(), prev.Month())
	}
	if next.Year() > maxYear {
		nav.NextDisabled = true
	} else {
		nav.NextURL = url(next.Year(), next.Month())
	}
	return nav
}

// fillMonthNav populates navigation for month view.
func (f *Figures) fillMonthNav(now, start time.Time) {
	nav := monthNav(now, start, monthURL)
	f.Mode = "month"
	f.Year, f.MonthNum, f.NavMonth = nav.Year, nav.MonthNum, nav.MonthNum
	f.Years, f.Months = nav.Years, nav.Months
	f.PrevURL, f.PrevDisabled = nav.PrevURL, nav.PrevDisabled
	f.NextURL, f.NextDisabled = nav.NextURL, nav.NextDisabled
	f.TodayURL = nav.TodayURL
	f.MonthViewURL = monthURL(start.Year(), start.Month())
	f.YearViewURL = fmt.Sprintf("/%d", start.Year())
	f.RefreshURL = f.MonthViewURL + "?refresh=1"
	f.MinimalToggleURL = f.MonthViewURL + "?minimal=toggle"
}

// fillYearNav populates navigation for year view. Switching to month view lands
// on the current month in the current year, otherwise January.
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
	f.NavMonth = int(month)
	f.MonthViewURL = monthURL(start.Year(), month)
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

// aggregatesInRange merges yd's per-month aggregates over [start, end],
// re-summing per project + rate. start and end lie in the same year; a nil yd
// (failed fetch) returns nil.
//
// Invoices deliberately do NOT suppress anything here: the Income panel records
// work done and still to come, and an invoice doesn't un-work those hours.
// Superseding them is the Rolling budget panel's job — see monthLaborIncome.
func aggregatesInRange(yd *YearData, start, end time.Time) []Aggregate {
	if yd == nil {
		return nil
	}
	type key struct{ pid, rate int }
	acc := map[key]*Aggregate{}
	var order []key
	for m := start.Month(); m <= end.Month(); m++ {
		for _, a := range yd.Months[m] {
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

// computeActuals decorates the built budget view with recorded spending. It
// runs between computeBudget and computePersonal, and touches no figure any
// downstream step reads.
func (f *Figures) computeActuals(t *Tracker, ctx context.Context, year int, start, now time.Time, months int, bv *BudgetView) {
	if t.Actuals == nil || f.BudgetErr != "" {
		return
	}

	var av ActualsView
	var err error
	if months > 1 {
		// For the current year ForYear projects private spend forward from
		// this month, so actuals beside it would compare the wrong halves.
		if year >= now.Year() {
			return
		}
		av, err = t.Actuals.ForYear(ctx, year)
	} else {
		av, err = t.Actuals.ForMonth(ctx, year, start.Month())
	}
	if err != nil {
		f.ActualsErr = err.Error()
		return
	}
	if !av.Present {
		return
	}

	// The mistimed check spans the year, so month view only.
	var charged map[string][]time.Month
	if months == 1 {
		if charged, err = t.Actuals.ChargedMonths(ctx, year); err != nil {
			f.ActualsErr = err.Error()
			return
		}
	}

	ApplyActuals(bv, av, start.Month(), charged)

	f.ShowActuals = true
	f.Mistimed = MistimedRowsOf(*bv)
	for _, g := range bv.Groups {
		f.PrivateActualCents += g.ActualCents
	}
	for _, g := range bv.CompanyGroups {
		f.CompanyActualCents += g.ActualCents
	}
	f.PrivateUnmatchedCents, f.CompanyUnmatchedCents = UnmatchedCents(*bv, av, t.companyCategoryIDs(ctx))
	f.PrivateActualCents += f.PrivateUnmatchedCents
	f.CompanyActualCents += f.CompanyUnmatchedCents
}

// companyCategoryIDs is the set of ids in company-kind groups, so unmatched
// spending lands in the ledger it came from. A failure only means it shows as
// private.
func (t *Tracker) companyCategoryIDs(ctx context.Context) map[string]bool {
	if t.Budget == nil {
		return nil
	}
	bf, err := t.Budget.File(ctx)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, g := range bf.Groups {
		if g.Kind != budgetdata.GroupKindCompany {
			continue
		}
		for _, c := range g.Categories {
			out[c.Id] = true
		}
	}
	return out
}
