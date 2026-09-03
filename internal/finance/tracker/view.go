package tracker

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

type Tracker struct {
	Toggl        HoursSource
	Holidays     *Holidays
	Budget       *Budget
	Accounts     *Accounts
	Actuals      *Actuals
	HoursPerDay  float64
	Loc          *time.Location
	Personal     PersonalParams
	VacationDays int
	RateCents    int
	RateCurrency string
	Invoiced     map[int]InvoicedClient
	Minimal      bool

	Start time.Time
}

func (t *Tracker) hours() HoursSource {
	if t.Toggl == nil {
		return (*Toggl)(nil)
	}
	return t.Toggl
}

func (t *Tracker) startMonth() yearMonth {
	if t == nil || t.Start.IsZero() {
		return yearMonth{}
	}
	return yearMonth{t.Start.Year(), t.Start.Month()}
}

type TrackedRow struct {
	Project     string
	Hours       string
	Rate        string
	AmountCents int
}

type InvoicedRow struct {
	Number      string
	AmountCents int
	URL         string
}

type HolidayView struct {
	Date    string
	Name    string
	Current bool
}

type MonthOption struct {
	Num  int
	Name string

	Untracked bool
}

type Figures struct {
	Month    string
	Currency string

	Login             string
	ReadOnly          bool
	ShowInvoicingLink bool
	ShowInfoLink      bool

	Mode         string
	Year         int
	MonthNum     int
	PrevURL      string
	NextURL      string
	PrevDisabled bool
	NextDisabled bool
	Years        []int
	Months       []MonthOption
	NavMonth     int
	MonthViewURL string
	YearViewURL  string
	TodayURL     string
	RefreshURL   string
	LastUpdated  string

	MinimalMode      bool
	MinimalToggleURL string

	TogglPending    bool
	TogglStaleNote  string
	TogglKeyNote    string
	TogglKeyExpired bool

	Tracked    []TrackedRow
	TrackedErr string

	Invoiced      []InvoicedRow
	InvoicedCents int

	ShowExpected     bool
	ExpectedRange    string
	ExpectedHours    string
	ExpectedRate     string
	ExpectedCents    int
	ExpectedErr      string
	ExpectedNetHours string
	ExpectedNetCents int

	ShowVacation          bool
	VacationTotal         int
	VacationTaken         int
	VacationRemaining     int
	VacationHoursDeducted string
	VacationCentsDeducted int
	VacationErr           string

	TotalHours string
	TotalRate  string
	TotalCents int
	TotalErr   string

	SpendableLabel string
	SpendableURL   string

	Holidays    []HolidayView
	HolidaysErr string

	Personal PersonalView

	PrivateGroups            []CategoryGroupView
	PrivateTotalPlannedCents int
	BudgetErr                string

	ShowActuals           bool
	PrivateActualCents    int
	CompanyActualCents    int
	PrivateUnmatchedCents int
	CompanyUnmatchedCents int
	Mistimed              []MistimedRow
	ActualsErr            string

	UntrackedCents    int
	UntrackedCount    int
	SpendingDetailURL string

	ShowSpendingLink bool

	FundingPersonal PersonalView

	ShowBalance  bool
	BalanceCents int

	ShowActualBalance         bool
	ActualBalanceCents        int
	ActualCompanyClosingCents int
	CompanyCashOutCents       int

	ShowOpeningBalance  bool
	OpeningBalanceCents int
	AvailableCents      int
	PrivateAccounts     []AccountRow
	CompanyAccounts     []AccountRow

	// ArrivedPrivatelyCents is the asset side of a draw, and ActualAvailable is
	// what it changes. They are Actual-column figures only: a draw is in no
	// plan, so it may not reach AvailableCents, which stays pure budget.
	ArrivedPrivatelyCents int
	ActualAvailableCents  int

	TargetNeedsBalanceNote   string
	DividendNeedsBalanceNote string

	ShowCompanyWorth  bool
	OwedToOwnerCents  int
	CompanyWorthCents int

	ShowDirectorLoan    bool
	LoanOpeningCents    int
	LoanNetIncomeCents  int
	LoanMovementCents   int
	LoanClosingCents    int
	DirectorLoanUnknown string
	DirectorLoanNotes   []string

	AccountsErr string
}

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

func (t *Tracker) ComputeMonth(ctx context.Context, year int, month time.Month) Figures {
	now := time.Now().In(t.Loc)
	start := time.Date(year, month, 1, 0, 0, 0, 0, t.Loc)
	end := start.AddDate(0, 1, -1)
	result := t.compute(ctx, year, start, end, start.Format("January 2006"), 1, 0)
	result.fillMonthNav(now, start, t.startMonth())
	return result
}

func (t *Tracker) ComputeYear(ctx context.Context, year int) Figures {
	now := time.Now().In(t.Loc)
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
	end := start.AddDate(1, 0, -1)
	result := t.compute(ctx, year, start, end, start.Format("2006"), 12, t.VacationDays)
	result.fillYearNav(now, start, t.startMonth())
	return result
}

func (t *Tracker) compute(ctx context.Context, year int, start, end time.Time, label string, months, vacationDays int) Figures {
	now := time.Now().In(t.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.Loc)

	result := Figures{Month: label, Currency: "€"}

	isCurrentPeriod := !today.Before(start) && !today.After(end)

	togglCtx, cancelToggl := waitBudget(ctx)
	defer cancelToggl()

	projects, perr := t.hours().Projects(togglCtx)
	yd, terr := t.hours().Year(togglCtx, year)
	result.TogglPending = terr != nil && t.hours().YearPending(year)
	aggs := aggregatesInRange(yd, start, end)
	todayErr := terr
	todayTracked := isCurrentPeriod && terr == nil && yd.Days[today.Format("2006-01-02")]
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
	result.ShowExpected = !today.After(end)

	result.computeTotal(trackedHours, trackedCents, expectedNetHours, expectedOK, rateCents)

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

	viewed := yearMonth{year, start.Month()}
	carried, snap, opened, accountsErr := t.carriedBalances(ctx, viewed, now, months, rateCents)
	result.AccountsErr = accountsErr
	result.TargetNeedsBalanceNote = t.targetNeedsBalanceNote(viewed, months, carried)
	result.DividendNeedsBalanceNote = t.dividendNeedsBalanceNote(viewed, months, carried, bv)

	result.computePersonal(t, ctx, year, months, viewed, monthlyCompanyCents, bv, carried.Company)

	result.computeSpendable(months, year, start, trackedHours > 0 || expectedNetHours > 0)

	result.computeFundingBalance(t, ctx, year, start, now, months, rateCents, bv, carried, snap, opened)

	result.computeDirectorLoan(t, ctx, viewed, months, carried)

	if at, stale := t.hours().YearStatus(year); !at.IsZero() {
		result.LastUpdated = at.In(t.Loc).Format("02 Jan 15:04")
		if stale {
			result.TogglStaleNote = "Toggl didn't answer — tracked hours are the last ones fetched, on " + result.LastUpdated + "."
		}
	} else {
		result.LastUpdated = "—"
	}
	if ks := t.hours().KeyStatus(today); ks.Warning != "" {
		result.TogglKeyNote, result.TogglKeyExpired = ks.Warning, ks.Expired
	}

	return result
}

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
		f.VacationTaken = freeWorkdays(start, today, holidaySet, yd.Days)
	}
	if ok {
		if rem := vacationDays - f.VacationTaken; rem > 0 {
			f.VacationRemaining = rem
		}
	}
}

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

func (f *Figures) computeInvoicedRows(t *Tracker, year int, start, end time.Time) {
	for m := start.Month(); m <= end.Month(); m++ {
		for _, inv := range t.invoicedInvoicesForMonth(yearMonth{year, m}) {
			f.Invoiced = append(f.Invoiced, InvoicedRow{Number: inv.Number, AmountCents: inv.Cents})
			f.InvoicedCents += inv.Cents
		}
	}
	sort.Slice(f.Invoiced, func(i, j int) bool { return f.Invoiced[i].Number < f.Invoiced[j].Number })
}

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

func (f *Figures) computeTotal(trackedHours float64, trackedCents int, expectedNetHours float64, expectedOK bool, rateCents int) {
	if f.TrackedErr == "" && expectedOK {
		f.TotalHours = formatCompactHours(trackedHours + expectedNetHours)
		f.TotalRate = formatNum(float64(rateCents) / 100)
		f.TotalCents = trackedCents + f.ExpectedNetCents
	} else {
		f.TotalErr = "unavailable"
	}
}

func (f *Figures) computeBudget(t *Tracker, ctx context.Context, year int, start, now time.Time, months int) BudgetView {
	var bv BudgetView
	if t.Budget == nil {
		return bv
	}
	var berr error
	if months > 1 {
		bv, berr = t.Budget.ForYear(ctx, year, now, t.Start)
	} else {
		f.MinimalMode = t.Minimal
		bv, berr = t.Budget.ForMonth(ctx, year, start.Month(), now, t.Minimal)
	}
	if berr != nil {
		f.BudgetErr = berr.Error()
	}
	return bv
}

func (f *Figures) computePersonal(t *Tracker, ctx context.Context, year, months int, viewed yearMonth, monthlyCompanyCents map[time.Month]int, bv BudgetView, company companyStock) {
	if f.TotalErr != "" {
		f.Personal = PersonalView{Err: "company income unavailable"}
		return
	}
	if months > 1 {
		var monthlyCompanyExpenseCents map[time.Month]int
		if t.Budget != nil && f.BudgetErr == "" {
			if m, err := t.Budget.CompanyExpensesByMonth(ctx, year, t.Start); err != nil {
				f.BudgetErr = err.Error()
			} else {
				monthlyCompanyExpenseCents = m
			}
		}
		first, _ := yearMonthRange(year, t.startMonth())
		start := time.Date(year, first, 1, 0, 0, 0, 0, t.Loc)
		end := time.Date(year, time.December, 31, 0, 0, 0, 0, t.Loc)
		f.Personal = t.Personal.withDividends(bv.Dividends).breakdownMonths(
			monthlyIncomeEUR(start, end, monthlyCompanyCents),
			monthlyIncomeEUR(start, end, monthlyCompanyExpenseCents),
			yearMonth{year, first},
			companyStock{},
		)
	} else {
		stock := t.Personal.targetStock(viewed, company)
		rules := t.Personal.rulesFor(viewed)
		due := bv.Dividends.dueIn(viewed)
		if problem := due.unrated(viewed, rules); problem != "" {
			f.Personal = PersonalView{Err: problem}
			return
		}
		f.Personal = t.Personal.breakdown(float64(f.TotalCents)/100, float64(bv.CompanyTotalPlannedCents)/100, 1,
			rules, t.Personal.decide(viewed, stock), stock, due)
	}
	f.Personal.CompanyGroups = bv.CompanyGroups
}

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

func (f *Figures) computeFundingBalance(t *Tracker, ctx context.Context, year int, start, now time.Time, months, rateCents int, bv BudgetView, carried openings, snap AccountSnapshot, opened bool) {
	var fundingStart, fundingEnd yearMonth
	if months > 1 {
		fundingStart, fundingEnd = fundingRangeForYear(year, now, t.startMonth())
	} else {
		fundingStart, fundingEnd = fundingRangeForMonth(year, start.Month())
	}
	f.FundingPersonal = t.fundingIncome(ctx, fundingStart, fundingEnd, now, rateCents, float64(bv.CompanyTotalPlannedCents)/100, bv.CompanyGroups, carried.Company, bv.Dividends)

	if t.Budget == nil {
		return
	}
	if f.BudgetErr == "" {
		f.PrivateGroups = bv.Groups
		f.PrivateTotalPlannedCents = bv.TotalPlannedCents
	}

	if opened {
		f.publishAccountBalances(snap, carried, yearMonth{year, start.Month()})
	}

	if f.BudgetErr == "" && f.FundingPersonal.Err == "" {
		f.ShowBalance = true
		f.BalanceCents = f.OpeningBalanceCents + f.FundingPersonal.NetIncomeCents - f.PrivateTotalPlannedCents
		f.publishBalanceTheBankSaw(months)
	}
}

// publishBalanceTheBankSaw answers "what does the account actually hold",
// which the planned figure cannot: the plan charges the whole month on the
// first, while the statements say what was really spent. Every month that has
// been imported can answer it — mid-month it is where today stands, and in a
// closed month it is what the month really ended on, which is the figure the
// next month opens with. Both are shown rather than one replacing the other,
// because the actual one is optimistic by whatever has not been imported or
// assigned yet, and only the pair says so.
//
// It is also where a draw is finally worth something. The plan can only assume
// the net salary arrived and knows of no other funding, so the planned column
// stays on that assumption; the Actual column starts from the same net salary
// and then adds what the statements say actually reached the owner besides it.
// The residual — net salary the company never transferred — is the director's
// loan's own movement row, so the two figures disagree by exactly the thing the
// loan is there to report, and neither has to guess.
func (f *Figures) publishBalanceTheBankSaw(months int) {
	if months != 1 || !f.ShowActuals {
		return
	}
	f.ShowActualBalance = true
	f.ActualAvailableCents = f.OpeningBalanceCents + f.FundingPersonal.NetSalaryCents() + f.ArrivedPrivatelyCents
	f.ActualBalanceCents = f.ActualAvailableCents - f.PrivateActualCents
	pv := f.FundingPersonal
	// The Actual column takes what the statements say left the bank, where the
	// planned one takes what the plan says will: the taxes are declared with
	// the distribution but only leave when they are actually paid, and an owner
	// draw is in no plan at all.
	month := pv.plannedCompanyMonth(pv.CompanyOpeningCents)
	month.ExpensesCents = f.CompanyActualCents
	month.CashOutCents = f.CompanyCashOutCents
	f.ActualCompanyClosingCents = month.closesAt()
}

func (f Figures) HeadlineBalanceCents() int {
	if f.ShowActualBalance {
		return f.ActualBalanceCents
	}
	return f.BalanceCents
}

func (f Figures) HeadlineAvailableCents() int {
	if f.ShowActualBalance {
		return f.ActualAvailableCents
	}
	return f.AvailableCents
}

func (f Figures) HeadlineCompanyClosingCents() int {
	if f.ShowActualBalance {
		return f.ActualCompanyClosingCents
	}
	return f.FundingPersonal.CompanyClosingCents
}

// targetNeedsBalanceNote covers the one way a target can be set and still do
// nothing without anybody being told: there is no company balance to measure
// it against. The month then pays whatever the salary block says, and the
// target reads as broken rather than as unfed.
func (t *Tracker) targetNeedsBalanceNote(viewed yearMonth, months int, carried openings) string {
	if months != 1 || carried.Company.Known {
		return ""
	}
	amount, inForce := t.Personal.Target.at(viewed)
	if !inForce {
		return ""
	}
	return "A target balance of " + formatEuro(round(amount*100)) + " is set for this month, but no company account is declared in accounts.json — " +
		"there is no balance to compare it against, so the target does nothing. Add the account with \"kind\": \"company\"."
}

// dividendNeedsBalanceNote covers the case where a distribution is charged
// against a pot nobody declared: with no company balance the whole "Left in
// the company" block disappears, so the money leaving it leaves no trace on
// the page beyond a salary that shrank for no visible reason.
func (t *Tracker) dividendNeedsBalanceNote(viewed yearMonth, months int, carried openings, bv BudgetView) string {
	if months != 1 || carried.Company.Known {
		return ""
	}
	due := bv.Dividends.dueIn(viewed)
	if due.none() {
		return ""
	}
	return "A dividend of " + formatEuro(round(due.AmountEUR*100)) + " is paid this month, but no company account is declared in accounts.json — " +
		"there is no balance to take it out of, so the company's closing figure is not shown. Its two taxes still reduce what a full salary can afford. " +
		"Add the account with \"kind\": \"company\"."
}

// computeDirectorLoan carries the running balance between the owner and the
// company into the month, adds what the company took on and takes off what
// actually crossed. It reaches no other figure on the page: the private
// balance still rolls forward assuming net income lands in the account, and
// this is precisely the number saying by how much that assumption is out.
func (f *Figures) computeDirectorLoan(t *Tracker, ctx context.Context, viewed yearMonth, months int, carried openings) {
	if months != 1 || t.Accounts == nil || f.AccountsErr != "" {
		return
	}
	if !carried.Loan.Known {
		f.DirectorLoanUnknown = "No opening figure is stated before this month, so what the company owes is not known — which is not the same as nothing. " +
			"Add a reading to director_loan in accounts.json."
		f.ShowDirectorLoan = t.hasDirectorLoanBlock(ctx)
		return
	}
	if f.FundingPersonal.Err != "" {
		f.DirectorLoanUnknown = "The month's net income could not be worked out, so what the company owes cannot be closed off either — see the error above."
		f.ShowDirectorLoan = t.hasDirectorLoanBlock(ctx)
		return
	}
	f.ShowDirectorLoan = true
	f.LoanOpeningCents = carried.Loan.OpeningCents
	f.LoanNetIncomeCents = f.FundingPersonal.NetIncomeCents
	f.LoanMovementCents = -t.crossedInMonth(ctx, viewed)
	f.LoanClosingCents = f.LoanOpeningCents + f.LoanNetIncomeCents + f.LoanMovementCents
	f.DirectorLoanNotes = t.directorLoanNotes(ctx, viewed)
	f.publishCompanyWorth()
}

// publishCompanyWorth puts the loan beside the company's accounts, which is
// where it belongs: a company holding nothing in the bank and owed a fortune by
// its owner is not a poor company. The loan is owner-centric — positive means
// the company owes him — so the company's side of the same figure is its
// negative, and the label turns over with it.
//
// Deliberately not folded into "In the company". That figure is what the
// cascade pays a salary from, and a salary cannot be paid out of money the
// owner has not given back.
func (f *Figures) publishCompanyWorth() {
	if !f.FundingPersonal.ShowCompanyBalance || f.DirectorLoanUnknown != "" {
		return
	}
	f.ShowCompanyWorth = true
	f.OwedToOwnerCents = -f.LoanOpeningCents
	f.CompanyWorthCents = f.FundingPersonal.CompanyOpeningCents + f.OwedToOwnerCents
}

// OwedLabel turns over with the direction, because the figure beside it is a
// bare number and the noun alone does not say which way it points. Both sides
// name it a loan: this is one director's-loan account that can sit either way
// up, and calling only one direction a loan would read as two separate things.
func (f Figures) OwedLabel() string {
	if f.OwedToOwnerCents < 0 {
		return "Loan from the owner"
	}
	return "Loan to the owner"
}

// ArrivedPrivatelyLabel turns over the same way, and says the money left the
// company rather than that it landed in an account: a draw taken as cash and
// never deposited is still the owner's to spend, so naming an account would be
// a claim the statements do not make.
func (f Figures) ArrivedPrivatelyLabel() string {
	if f.ArrivedPrivatelyCents < 0 {
		return "Paid into the company"
	}
	return "Drawn from the company"
}

// directorLoanNotes says the one thing about the figure nobody can work out by
// reading it: two lines marked for a single transfer count that transfer twice.
// It is a warning about the data, not a description of the arithmetic.
func (t *Tracker) directorLoanNotes(ctx context.Context, viewed yearMonth) []string {
	var notes []string
	if t.Actuals != nil {
		if av, err := t.Actuals.ForMonth(ctx, viewed.Year, viewed.Month); err == nil && av.DoubleMarked {
			notes = append(notes, "Two lines this month are marked as the same movement, for the same amount, on the same day. If they are one transfer recorded from both statements, unmark the private side — it is counted twice here.")
		}
	}
	return notes
}

// crossedInMonth is the viewed month's own settlements. The month's actuals
// are already cached by everything else on the page, so this costs no read.
func (t *Tracker) crossedInMonth(ctx context.Context, viewed yearMonth) int {
	if t.Actuals == nil {
		return 0
	}
	av, err := t.Actuals.ForMonth(ctx, viewed.Year, viewed.Month)
	if err != nil {
		return 0
	}
	return av.CrossedCents
}

// hasDirectorLoanBlock decides whether "not known" is worth saying at all: a
// file that has never mentioned the loan is not being told anything by a note
// about a figure it does not track.
func (t *Tracker) hasDirectorLoanBlock(ctx context.Context) bool {
	af, err := t.Accounts.File(ctx)
	return err == nil && af.DirectorLoan != nil
}

// carriedBalances rolls both pots up to the month being looked at. It runs
// before the cascade rather than after it, because the company balance is one
// of the cascade's inputs now — what a full salary can afford includes what the
// company is already holding.
func (t *Tracker) carriedBalances(ctx context.Context, viewed yearMonth, now time.Time, months, rateCents int) (openings, AccountSnapshot, bool, string) {
	if t.Accounts == nil || months != 1 {
		return openings{}, AccountSnapshot{}, false, ""
	}
	af, err := t.Accounts.File(ctx)
	if err != nil {
		return openings{}, AccountSnapshot{}, false, err.Error()
	}
	snap, ok := snapshotFor(af, viewed)
	loan := directorLoanInForce(af, viewed)
	// Either anchor is enough to walk: the loan can be stated when no bank
	// balance has been read yet, and it opens on its own reading regardless.
	if !ok && !loan.Known {
		return openings{}, AccountSnapshot{}, false, ""
	}
	carried, err := t.rollForward(ctx, snap, loan, viewed, now, rateCents)
	if err != nil {
		return openings{}, AccountSnapshot{}, false, err.Error()
	}
	return carried, snap, ok, ""
}

func (f *Figures) publishAccountBalances(snap AccountSnapshot, carried openings, viewed yearMonth) {
	if f.BudgetErr != "" || f.FundingPersonal.Err != "" {
		return
	}
	f.ShowOpeningBalance = true
	f.OpeningBalanceCents = carried.PrivateCents
	f.AvailableCents = carried.PrivateCents + f.FundingPersonal.NetIncomeCents
	if viewed == snap.OpensMonth {
		f.PrivateAccounts = snap.rowsOfKind(accountsdata.AccountKindPrivate)
		f.CompanyAccounts = snap.rowsOfKind(accountsdata.AccountKindCompany)
	}
}

func (t *Tracker) EvictMonth(year int, month time.Month) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, t.Loc)
	t.hours().EvictRange(start, start.AddDate(0, 1, -1))
	fs, fe := fundingRangeForMonth(year, month)
	t.evictFundingRange(fs, fe)
	if t.Budget != nil {
		t.Budget.Evict()
	}
	t.Accounts.Evict()
	t.Actuals.Evict()
}

func (t *Tracker) EvictYear(year int) {
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, t.Loc)
	t.hours().EvictRange(start, start.AddDate(1, 0, -1))
	fs, fe := fundingRangeForYear(year, time.Now().In(t.Loc), t.startMonth())
	t.evictFundingRange(fs, fe)
	if t.Budget != nil {
		t.Budget.Evict()
	}
	t.Accounts.Evict()
	t.Actuals.Evict()
}

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

func monthNav(now, start time.Time, floor yearMonth, url func(int, time.Month) string) MonthNav {
	prev, next := start.AddDate(0, -1, 0), start.AddDate(0, 1, 0)
	minYear, maxYear := navYearBounds(now, floor)
	nav := MonthNav{
		Year:     start.Year(),
		MonthNum: int(start.Month()),
		Years:    navYears(now, floor),
		TodayURL: url(now.Year(), now.Month()),
	}
	for m := time.January; m <= time.December; m++ {
		if floor != (yearMonth{}) && (yearMonth{start.Year(), m}).ordinal() < floor.ordinal() {
			continue
		}
		nav.Months = append(nav.Months, MonthOption{Num: int(m), Name: m.String()})
	}
	if prev.Year() < minYear || (floor != (yearMonth{}) && (yearMonth{prev.Year(), prev.Month()}).ordinal() < floor.ordinal()) {
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

func markUntrackedMonths(months []MonthOption, untracked map[time.Month]int) {
	for i := range months {
		if untracked[time.Month(months[i].Num)] != 0 {
			months[i].Untracked = true
		}
	}
}

func (f *Figures) fillMonthNav(now, start time.Time, floor yearMonth) {
	nav := monthNav(now, start, floor, monthURL)
	f.Mode = "month"
	f.Year, f.MonthNum, f.NavMonth = nav.Year, nav.MonthNum, nav.MonthNum
	f.Years, f.Months = nav.Years, nav.Months
	f.PrevURL, f.PrevDisabled = nav.PrevURL, nav.PrevDisabled
	f.NextURL, f.NextDisabled = nav.NextURL, nav.NextDisabled
	f.TodayURL = nav.TodayURL
	f.MonthViewURL = monthURL(start.Year(), start.Month())
	f.YearViewURL = fmt.Sprintf("/%d", start.Year())
	f.RefreshURL = "/refresh?return=" + url.QueryEscape(f.MonthViewURL) +
		fmt.Sprintf("&year=%d&month=%d", f.Year, f.NavMonth)
	f.MinimalToggleURL = "/minimal?return=" + url.QueryEscape(f.MonthViewURL)
}

func (f *Figures) fillYearNav(now, start time.Time, floor yearMonth) {
	minYear, maxYear := navYearBounds(now, floor)
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
	f.Years = navYears(now, floor)
	f.YearViewURL = fmt.Sprintf("/%d", start.Year())
	month := time.January
	if start.Year() == now.Year() {
		month = now.Month()
	} else if floor != (yearMonth{}) && floor.Year == start.Year() {
		month = floor.Month
	}
	f.NavMonth = int(month)
	f.MonthViewURL = monthURL(start.Year(), month)
	f.TodayURL = fmt.Sprintf("/%d/%d", now.Year(), int(now.Month()))
	f.RefreshURL = "/refresh?return=" + url.QueryEscape(f.YearViewURL) +
		fmt.Sprintf("&year=%d", f.Year)
}

func navYears(now time.Time, start yearMonth) []int {
	minYear, maxYear := navYearBounds(now, start)
	years := make([]int, 0, maxYear-minYear+1)
	for y := minYear; y <= maxYear; y++ {
		years = append(years, y)
	}
	return years
}

func navYearBounds(now time.Time, start yearMonth) (int, int) {
	const yearRange = 2
	minYear := now.Year() - yearRange
	if start != (yearMonth{}) && start.Year > minYear {
		minYear = start.Year
	}
	return minYear, now.Year() + yearRange
}

func NavBounds(now, start time.Time) (minYear, maxYear int) {
	var ym yearMonth
	if !start.IsZero() {
		ym = yearMonth{start.Year(), start.Month()}
	}
	return navYearBounds(now, ym)
}

func MonthIsOffered(year, month int, now, start time.Time) bool {
	minYear, maxYear := NavBounds(now, start)
	if year < minYear || year > maxYear || month < 1 || month > 12 {
		return false
	}
	if start.IsZero() {
		return true
	}
	first := yearMonth{start.Year(), start.Month()}
	return yearMonth{year, time.Month(month)}.ordinal() >= first.ordinal()
}

func aggHours(a Aggregate) float64 {
	if a.RateCents > 0 {
		return float64(a.AmountCents) / float64(a.RateCents)
	}
	return float64(a.Seconds) / 3600
}

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

func (f *Figures) computeActuals(t *Tracker, ctx context.Context, year int, start, now time.Time, months int, bv *BudgetView) {
	if t.Actuals == nil || f.BudgetErr != "" {
		return
	}

	if untracked, uerr := t.Actuals.UntrackedMonths(ctx, year, t.Start); uerr == nil {
		markUntrackedMonths(f.Months, untracked)
	}

	var av ActualsView
	var err error
	if months > 1 {
		if year >= now.Year() {
			return
		}
		av, err = t.Actuals.ForYear(ctx, year, t.Start)
	} else {
		av, err = t.Actuals.ForMonth(ctx, year, start.Month())
	}
	if err != nil {
		f.ActualsErr = err.Error()
		return
	}
	f.UntrackedCents, f.UntrackedCount = av.UntrackedCents, av.UntrackedCount

	if !av.Present {
		return
	}

	var charged map[string][]time.Month
	if months == 1 {
		if charged, err = t.Actuals.ChargedMonths(ctx, year, t.Start); err != nil {
			f.ActualsErr = err.Error()
			return
		}
	}

	ApplyActuals(bv, av, year, start.Month(), charged)

	f.ShowActuals = true
	f.Mistimed = MistimedRowsOf(*bv)
	companyIDs := t.companyCategoryIDs(ctx)
	f.PrivateUnmatchedCents, f.CompanyUnmatchedCents = UnmatchedCents(*bv, av, companyIDs)
	f.PrivateActualCents, f.CompanyActualCents = ActualTotals(av, companyIDs)
	f.CompanyCashOutCents = av.CompanyCashOutCents
	f.ArrivedPrivatelyCents = av.ArrivedPrivatelyCents
}

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
