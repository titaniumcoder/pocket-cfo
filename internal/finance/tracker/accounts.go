package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sync"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
)

const accountsPath = "accounts.json"

type Accounts struct {
	FS fs.FS

	mu        sync.Mutex
	cache     *accountsResult
	justWrote committed
}

func (a *Accounts) Publish(body []byte) {
	if a == nil {
		return
	}
	a.justWrote.publish(accountsPath, body)
	a.Evict()
}

type accountsResult struct {
	file accountsdata.AccountsFile
	err  error
}

type AccountSnapshot struct {
	OpensMonth   yearMonth
	PrivateCents int
	CompanyCents int
	HasCompany   bool
	Found        bool
	AccountRow   []AccountRow
	LatestAsOf   time.Time
}

type AccountRow struct {
	Name  string
	Kind  accountsdata.AccountKind
	Cents int
	AsOf  string
}

func (s AccountSnapshot) rowsOfKind(kind accountsdata.AccountKind) []AccountRow {
	var out []AccountRow
	for _, row := range s.AccountRow {
		if row.Kind == kind {
			out = append(out, row)
		}
	}
	return out
}

func (a *Accounts) File(ctx context.Context) (accountsdata.AccountsFile, error) {
	if a == nil {
		return accountsdata.AccountsFile{}, nil
	}
	a.mu.Lock()
	if a.cache != nil {
		cached := a.cache
		a.mu.Unlock()
		return cached.file, cached.err
	}
	a.mu.Unlock()

	log.Printf("accounts: %s — fetching…", accountsPath)
	af, err := a.fetch()
	if err != nil {
		log.Printf("accounts: %s — failed: %v", accountsPath, err)
	} else {
		log.Printf("accounts: %s — %d account(s)", accountsPath, len(af.Accounts))
	}

	a.mu.Lock()
	a.cache = &accountsResult{file: af, err: err}
	a.mu.Unlock()
	return af, err
}

func (a *Accounts) readFile() ([]byte, error) {
	if a.FS == nil {
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(a.FS, accountsPath)
}

func (a *Accounts) fetch() (accountsdata.AccountsFile, error) {
	content, err := a.readFile()
	if body, ok := a.justWrote.supersedes(accountsPath, content, err); ok {
		content, err = body, nil
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return accountsdata.AccountsFile{}, nil
		}
		return accountsdata.AccountsFile{}, fmt.Errorf("accounts: reading %s: %w", accountsPath, err)
	}
	var af accountsdata.AccountsFile
	if err := json.Unmarshal(content, &af); err != nil {
		return accountsdata.AccountsFile{}, fmt.Errorf("accounts: parse %s: %w", accountsPath, err)
	}
	if err := accountsdata.ValidateAccounts(af); err != nil {
		return accountsdata.AccountsFile{}, fmt.Errorf("accounts: %s: %w", accountsPath, err)
	}
	return af, nil
}

func (a *Accounts) Evict() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = nil
}

func (s *AccountSnapshot) addBalance(kind accountsdata.AccountKind, cents int) {
	if kind == accountsdata.AccountKindCompany {
		s.CompanyCents += cents
		s.HasCompany = true
		return
	}
	s.PrivateCents += cents
}

func readingInForce(acc accountsdata.Account, viewed yearMonth) (accountsdata.Reading, time.Time, bool) {
	return newestReadingBefore(acc.Balances, viewed)
}

// newestReadingBefore is the whole rule a series of readings follows, and the
// director's loan follows it too: a reading closes its month and opens the
// next, the newest one before the month being looked at wins, and a month
// before every reading has nothing in force — which is not the same as zero.
func newestReadingBefore(readings []accountsdata.Reading, viewed yearMonth) (accountsdata.Reading, time.Time, bool) {
	var best accountsdata.Reading
	var bestDate time.Time
	found := false
	for _, r := range readings {
		d, err := time.Parse("2006-01-02", r.AsOf)
		if err != nil {
			continue
		}
		opens := yearMonth{d.Year(), d.Month()}.addMonths(1)
		if opens.ordinal() > viewed.ordinal() {
			continue
		}
		if !found || d.After(bestDate) {
			best, bestDate, found = r, d, true
		}
	}
	return best, bestDate, found
}

// directorLoanStock is the loan's own anchor, resolved independently of the
// bank readings: the two are unrelated, and either can be known while the
// other is not.
type directorLoanStock struct {
	Known        bool
	OpeningCents int
	OpensMonth   yearMonth

	// UnimportedMonths counts the walked months whose statements are missing
	// or half-read. An unimported month settles nothing, so the figure is
	// optimistically high by whatever crossed in them — honest, but only if
	// the page says so.
	UnimportedMonths int
}

func directorLoanInForce(af accountsdata.AccountsFile, viewed yearMonth) directorLoanStock {
	if af.DirectorLoan == nil {
		return directorLoanStock{}
	}
	reading, d, ok := newestReadingBefore(af.DirectorLoan.Balances, viewed)
	if !ok {
		return directorLoanStock{}
	}
	return directorLoanStock{
		Known:        true,
		OpeningCents: round(reading.Balance * 100),
		OpensMonth:   yearMonth{d.Year(), d.Month()}.addMonths(1),
	}
}

func snapshotFor(af accountsdata.AccountsFile, viewed yearMonth) (AccountSnapshot, bool) {
	var snap AccountSnapshot
	found := false
	for _, acc := range af.Accounts {
		reading, d, ok := readingInForce(acc, viewed)
		if !ok {
			continue
		}
		opens := yearMonth{d.Year(), d.Month()}.addMonths(1)
		cents := round(reading.Balance * 100)
		snap.addBalance(acc.Kind, cents)
		snap.AccountRow = append(snap.AccountRow, AccountRow{
			Name:  acc.Name,
			Kind:  acc.Kind,
			Cents: cents,
			AsOf:  formatDay(reading.AsOf),
		})
		if !found || opens.ordinal() > snap.OpensMonth.ordinal() {
			snap.OpensMonth = opens
		}
		if d.After(snap.LatestAsOf) {
			snap.LatestAsOf = d
		}
		found = true
	}
	snap.Found = found
	return snap, found
}

func (a *Accounts) Snapshot(ctx context.Context, viewed yearMonth) (AccountSnapshot, bool, error) {
	af, err := a.File(ctx)
	if err != nil {
		return AccountSnapshot{}, false, err
	}
	snap, ok := snapshotFor(af, viewed)
	return snap, ok, nil
}

const maxRollForwardMonths = 120

type openings struct {
	PrivateCents int
	Company      companyStock
	Loan         directorLoanStock
}

// rollForward walks the months between the read date and the one being looked
// at, carrying both pots at once. They cannot be walked separately: the
// company balance is an input to the payroll cascade, and the private balance
// is fed by what that cascade pays out.
func (t *Tracker) rollForward(ctx context.Context, snap AccountSnapshot, loan directorLoanStock, viewed yearMonth, now time.Time, rateCents int) (openings, error) {
	from := walkStart(snap, loan, viewed)
	if elapsed := viewed.ordinal() - from.ordinal(); elapsed > maxRollForwardMonths {
		return openings{}, fmt.Errorf("accounts: a reading is %d months before this month (%s) — check the dates in accounts.json, that looks like a typo",
			elapsed, from)
	}

	open := openings{
		PrivateCents: snap.PrivateCents,
		Company:      companyStock{Known: snap.HasCompany, OpeningCents: snap.CompanyCents},
		Loan:         loan,
	}
	for m := from; m.ordinal() < viewed.ordinal(); m = m.addMonths(1) {
		closed, err := t.monthClose(ctx, m, now, rateCents, open.Company)
		if err != nil {
			return openings{}, err
		}
		// Each figure starts accruing only from its own anchor: the loan's
		// reading and the bank's are unrelated, and either may be the earlier
		// one. The months before a pot's own anchor contribute nothing to it.
		if snap.Found && m.ordinal() >= snap.OpensMonth.ordinal() {
			open.PrivateCents += closed.PrivateDeltaCents
			open.Company.OpeningCents = closed.CompanyClosingCents
		}
		if loan.Known && m.ordinal() >= loan.OpensMonth.ordinal() {
			open.Loan.OpeningCents += closed.NetIncomeCents - closed.CrossedCents
			if !closed.Imported {
				open.Loan.UnimportedMonths++
			}
		}
	}
	return open, nil
}

// walkStart is the earlier of the two anchors, because the loan and the bank
// balances are anchored independently and a loan stated further back would
// otherwise never accrue the months in between.
func walkStart(snap AccountSnapshot, loan directorLoanStock, viewed yearMonth) yearMonth {
	switch {
	case snap.Found && loan.Known && loan.OpensMonth.ordinal() < snap.OpensMonth.ordinal():
		return loan.OpensMonth
	case snap.Found:
		return snap.OpensMonth
	case loan.Known:
		return loan.OpensMonth
	}
	return viewed
}

type monthClosing struct {
	PrivateDeltaCents   int
	CompanyClosingCents int
	NetIncomeCents      int
	CrossedCents        int
	Imported            bool
}

func (t *Tracker) monthClose(ctx context.Context, m yearMonth, now time.Time, rateCents int, company companyStock) (monthClosing, error) {
	bv, err := t.Budget.ForMonth(ctx, m.Year, m.Month, now)
	if err != nil {
		return monthClosing{}, fmt.Errorf("accounts: budget for %s: %w", m, err)
	}
	privateCents, companyCents, av := t.closingExpenses(ctx, m, bv)
	fundingStart, fundingEnd := fundingRangeForMonth(m.Year, m.Month)
	pv := t.fundingIncome(ctx, fundingStart, fundingEnd, now, rateCents, float64(companyCents)/100, bv.CompanyGroups, company, bv.Dividends)
	if pv.Err != "" {
		return monthClosing{}, fmt.Errorf("accounts: income for %s: %s", m, pv.Err)
	}
	imported := av.Present && av.Complete
	return monthClosing{
		PrivateDeltaCents:   pv.NetIncomeCents - privateCents,
		CompanyClosingCents: companyClosedOn(pv, av, imported),
		NetIncomeCents:      pv.NetIncomeCents,
		CrossedCents:        av.CrossedCents,
		Imported:            imported,
	}, nil
}

// companyClosedOn seeds the next month, so it follows the same all-or-nothing
// rule the expenses follow: a fully imported month closes on what the bank
// says left — the draws and the taxes actually paid — and any other month
// closes on the plan, which charges the taxes a distribution declared and
// knows nothing of draws. Mixing them would charge a declared tax and an
// imported part of that same tax into a figure that is then carried for good.
func companyClosedOn(pv PersonalView, av ActualsView, imported bool) int {
	if !imported || !pv.ShowCompanyBalance {
		return pv.CompanyClosingCents
	}
	month := pv.plannedCompanyMonth(pv.CompanyOpeningCents)
	month.CashOutCents = av.CompanyCashOutCents
	return month.closesAt()
}

// closingExpenses hands back the view it read as well as the two figures, so
// the walk still costs one read of a month's actuals. The expenses fall back
// to the plan when nothing is imported, and so does the company's cash; the
// loan's crossings never do, because the plan holds no transfers at all and an
// unimported month has settled nothing.
func (t *Tracker) closingExpenses(ctx context.Context, m yearMonth, bv BudgetView) (private, company int, av ActualsView) {
	av, err := t.Actuals.ForMonth(ctx, m.Year, m.Month)
	if err != nil || !av.Present || !av.Complete {
		return bv.TotalPlannedCents, bv.CompanyTotalPlannedCents, av
	}
	private, company = ActualTotals(av, t.companyCategoryIDs(ctx))
	return private, company, av
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
