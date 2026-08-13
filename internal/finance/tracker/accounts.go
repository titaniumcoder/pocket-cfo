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

	mu    sync.Mutex
	cache *accountsResult
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
	AccountRow   []AccountRow
	LatestAsOf   time.Time
	NewestAsOf   time.Time
}

const staleAfterDays = 40

func (s AccountSnapshot) StaleDays(now time.Time) (int, bool) {
	days := int(now.Sub(s.NewestAsOf).Hours() / 24)
	return days, days > staleAfterDays
}

type AccountRow struct {
	Name   string
	Kind   accountsdata.AccountKind
	Cents  int
	Note   string
	AsOf   string
	Behind bool
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
	if a == nil || a.FS == nil {
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

func (a *Accounts) fetch() (accountsdata.AccountsFile, error) {
	content, err := fs.ReadFile(a.FS, accountsPath)
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
	var best accountsdata.Reading
	var bestDate time.Time
	found := false
	for _, r := range acc.Balances {
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

func newestReading(af accountsdata.AccountsFile) time.Time {
	var newest time.Time
	for _, acc := range af.Accounts {
		for _, r := range acc.Balances {
			d, err := time.Parse("2006-01-02", r.AsOf)
			if err != nil {
				continue
			}
			if d.After(newest) {
				newest = d
			}
		}
	}
	return newest
}

func snapshotFor(af accountsdata.AccountsFile, viewed yearMonth) (AccountSnapshot, bool) {
	snap := AccountSnapshot{NewestAsOf: newestReading(af)}
	var opensPerRow []int
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
			Note:  derefStr(reading.Note),
			AsOf:  formatDay(reading.AsOf),
		})
		opensPerRow = append(opensPerRow, opens.ordinal())
		if !found || opens.ordinal() > snap.OpensMonth.ordinal() {
			snap.OpensMonth = opens
		}
		if d.After(snap.LatestAsOf) {
			snap.LatestAsOf = d
		}
		found = true
	}
	for i := range snap.AccountRow {
		snap.AccountRow[i].Behind = opensPerRow[i] < snap.OpensMonth.ordinal()
	}
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
}

// rollForward walks the months between the read date and the one being looked
// at, carrying both pots at once. They cannot be walked separately: the
// company balance is an input to the payroll cascade, and the private balance
// is fed by what that cascade pays out.
func (t *Tracker) rollForward(ctx context.Context, snap AccountSnapshot, viewed yearMonth, now time.Time, rateCents int) (openings, error) {
	elapsed := viewed.ordinal() - snap.OpensMonth.ordinal()
	if elapsed > maxRollForwardMonths {
		return openings{}, fmt.Errorf("accounts: as_of is %d months before this month (%s) — check the date in accounts.json, that looks like a typo",
			elapsed, snap.LatestAsOf.Format("2006-01-02"))
	}

	open := openings{
		PrivateCents: snap.PrivateCents,
		Company:      companyStock{Known: snap.HasCompany, OpeningCents: snap.CompanyCents},
	}
	for m := snap.OpensMonth; m.ordinal() < viewed.ordinal(); m = m.addMonths(1) {
		closed, err := t.monthClose(ctx, m, now, rateCents, open.Company)
		if err != nil {
			return openings{}, err
		}
		open.PrivateCents += closed.PrivateDeltaCents
		open.Company.OpeningCents = closed.CompanyClosingCents
	}
	return open, nil
}

type monthClosing struct {
	PrivateDeltaCents   int
	CompanyClosingCents int
}

func (t *Tracker) monthClose(ctx context.Context, m yearMonth, now time.Time, rateCents int, company companyStock) (monthClosing, error) {
	bv, err := t.Budget.ForMonth(ctx, m.Year, m.Month, now)
	if err != nil {
		return monthClosing{}, fmt.Errorf("accounts: budget for %s: %w", m, err)
	}
	privateCents, companyCents := t.closingExpenses(ctx, m, bv)
	fundingStart, fundingEnd := fundingRangeForMonth(m.Year, m.Month)
	pv := t.fundingIncome(ctx, fundingStart, fundingEnd, now, rateCents, float64(companyCents)/100, bv.CompanyGroups, company)
	if pv.Err != "" {
		return monthClosing{}, fmt.Errorf("accounts: income for %s: %s", m, pv.Err)
	}
	return monthClosing{
		PrivateDeltaCents:   pv.NetIncomeCents - privateCents,
		CompanyClosingCents: pv.CompanyClosingCents,
	}, nil
}

func (t *Tracker) closingExpenses(ctx context.Context, m yearMonth, bv BudgetView) (private, company int) {
	av, err := t.Actuals.ForMonth(ctx, m.Year, m.Month)
	if err != nil || !av.Present || !av.Complete {
		return bv.TotalPlannedCents, bv.CompanyTotalPlannedCents
	}
	return ActualTotals(av, t.companyCategoryIDs(ctx))
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
