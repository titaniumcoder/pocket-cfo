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
	OpensMonth yearMonth
	Cents      int
	AccountRow []AccountRow
	LatestAsOf time.Time
}

const staleAfterDays = 40

func (s AccountSnapshot) StaleDays(now time.Time) (int, bool) {
	days := int(now.Sub(s.LatestAsOf).Hours() / 24)
	return days, days > staleAfterDays
}

type AccountRow struct {
	Name  string
	Cents int
	Note  string
	AsOf  string
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

func snapshotFor(af accountsdata.AccountsFile) (AccountSnapshot, bool) {
	var snap AccountSnapshot
	found := false
	for _, acc := range af.Accounts {
		d, err := time.Parse("2006-01-02", acc.AsOf)
		if err != nil {
			continue
		}
		opens := yearMonth{d.Year(), d.Month()}.addMonths(1)
		cents := round(acc.Balance * 100)
		snap.Cents += cents
		snap.AccountRow = append(snap.AccountRow, AccountRow{
			Name:  acc.Name,
			Cents: cents,
			Note:  derefStr(acc.Note),
			AsOf:  formatDay(acc.AsOf),
		})
		if !found || opens.ordinal() > snap.OpensMonth.ordinal() {
			snap.OpensMonth = opens
		}
		if d.After(snap.LatestAsOf) {
			snap.LatestAsOf = d
		}
		found = true
	}
	return snap, found
}

func (a *Accounts) Snapshot(ctx context.Context) (AccountSnapshot, bool) {
	af, err := a.File(ctx)
	if err != nil {
		return AccountSnapshot{}, false
	}
	return snapshotFor(af)
}

const maxRollForwardMonths = 120

func (t *Tracker) privateOpeningCents(ctx context.Context, snap AccountSnapshot, viewed yearMonth, now time.Time, rateCents int) (int, error) {
	elapsed := viewed.ordinal() - snap.OpensMonth.ordinal()
	if elapsed > maxRollForwardMonths {
		return 0, fmt.Errorf("accounts: as_of is %d months before this month (%s) — check the date in accounts.json, that looks like a typo",
			elapsed, snap.LatestAsOf.Format("2006-01-02"))
	}

	opening := snap.Cents
	for m := snap.OpensMonth; m.ordinal() < viewed.ordinal(); m = m.addMonths(1) {
		delta, err := t.monthBalanceDelta(ctx, m, now, rateCents)
		if err != nil {
			return 0, err
		}
		opening += delta
	}
	return opening, nil
}

func (t *Tracker) monthBalanceDelta(ctx context.Context, m yearMonth, now time.Time, rateCents int) (int, error) {
	bv, err := t.Budget.ForMonth(ctx, m.Year, m.Month, now)
	if err != nil {
		return 0, fmt.Errorf("accounts: budget for %s: %w", m, err)
	}
	fundingStart, fundingEnd := fundingRangeForMonth(m.Year, m.Month)
	pv := t.fundingIncome(ctx, fundingStart, fundingEnd, now, rateCents, float64(bv.CompanyTotalPlannedCents)/100, bv.CompanyGroups)
	if pv.Err != "" {
		return 0, fmt.Errorf("accounts: income for %s: %s", m, pv.Err)
	}
	return pv.NetIncomeCents - bv.TotalPlannedCents, nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
