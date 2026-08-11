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

// Accounts reads real bank balances from accounts.json, same convention as
// Budget. Unlike Budget, a missing file is not an error: this is an optional
// layer, and without it the dashboard behaves as it did before it existed.
type Accounts struct {
	// A nil FS means not configured, as with Tracker.Toggl.
	FS fs.FS

	mu    sync.Mutex
	cache *accountsResult
}

type accountsResult struct {
	file accountsdata.AccountsFile
	err  error
}

// AccountSnapshot is the combined balance and the month it OPENS. Accounts are
// summed and anchored at the latest as_of among them, since they're read at one
// sitting and the roll-forward needs a single effective date.
//
// OpensMonth is deliberately the month AFTER as_of: a balance read on 31 July
// is July's closing figure, so it is August's opening one.
type AccountSnapshot struct {
	OpensMonth yearMonth
	Cents      int
	AccountRow []AccountRow
	// A full date, not just its month, so StaleDays can count real days.
	LatestAsOf time.Time
}

// staleAfterDays is how long a balance may go unread before the dashboard says
// so. Everything from the snapshot month on is extrapolated from that one
// figure, and the drift is silent, which is the kind worth nagging about.
const staleAfterDays = 40

// StaleDays counts against real current time, not the viewed month: browsing to
// March doesn't make March's data fresh.
func (s AccountSnapshot) StaleDays(now time.Time) (int, bool) {
	days := int(now.Sub(s.LatestAsOf).Hours() / 24)
	return days, days > staleAfterDays
}

// AccountRow is one account as displayed.
type AccountRow struct {
	Name  string
	Cents int
	Note  string
	AsOf  string
}

// File returns the cached accounts.json, reading it on first use. A missing
// file yields a zero AccountsFile and no error (see Accounts).
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
			// Optional layer: absence is "not configured", not a failure.
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

// Evict lets the Reload link pick up a hand-edited balance without a restart.
func (a *Accounts) Evict() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = nil
}

// snapshotFor combines every account into one balance anchored at the latest
// as_of, shifted forward a month (see AccountSnapshot.OpensMonth). ok is false
// when no account has a parseable date, meaning no balance layer at all.
func snapshotFor(af accountsdata.AccountsFile) (AccountSnapshot, bool) {
	var snap AccountSnapshot
	found := false
	for _, acc := range af.Accounts {
		d, err := time.Parse("2006-01-02", acc.AsOf)
		if err != nil {
			continue // a malformed date drops that account, not the page
		}
		// The read month closes; the next one opens with this money.
		opens := yearMonth{d.Year(), d.Month()}.addMonths(1)
		cents := round(acc.Balance * 100)
		snap.Cents += cents
		snap.AccountRow = append(snap.AccountRow, AccountRow{
			Name:  acc.Name,
			Cents: cents,
			Note:  derefStr(acc.Note),
			AsOf:  acc.AsOf,
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

// Snapshot is the combined personal balance, or ok=false when none is
// configured.
func (a *Accounts) Snapshot(ctx context.Context) (AccountSnapshot, bool) {
	af, err := a.File(ctx)
	if err != nil {
		return AccountSnapshot{}, false
	}
	return snapshotFor(af)
}

// maxRollForwardMonths guards against a mistyped year, and is NOT a staleness
// policy — however long you've gone without reading your bank, the balance is
// still carried and shown; staleAfterDays is what says you're overdue.
//
// A typo like "1999-07-31" is different: each calendar year the loop crosses
// pulls that year's Toggl report and holiday list, so a century-wide span fans
// out into hundreds of API calls.
const maxRollForwardMonths = 120

// privateOpeningCents is the balance carried forward to the START of viewed:
// the snapshot itself in the month it opens, otherwise the previous month's
// closing figure (opening + net income - private expenses), compounded one
// month at a time. Callers guarantee viewed is at or after snap.OpensMonth.
func (t *Tracker) privateOpeningCents(ctx context.Context, snap AccountSnapshot, viewed yearMonth, now time.Time, rateCents int) (int, error) {
	elapsed := viewed.ordinal() - snap.OpensMonth.ordinal()
	if elapsed > maxRollForwardMonths {
		return 0, fmt.Errorf("accounts: as_of is %d months before this month (%s) — check the date in accounts.json, that looks like a typo",
			elapsed, snap.LatestAsOf.Format("2006-01-02"))
	}

	opening := snap.Cents
	// The viewed month itself is not applied: this is its OPENING figure, and
	// Figures.BalanceCents closes it.
	for m := snap.OpensMonth; m.ordinal() < viewed.ordinal(); m = m.addMonths(1) {
		delta, err := t.monthBalanceDelta(ctx, m, now, rateCents)
		if err != nil {
			return 0, err
		}
		opening += delta
	}
	return opening, nil
}

// monthBalanceDelta is m's net income minus its private expenses — the same two
// figures the Expenses panel shows for m, so browsing there and reading its
// Balance reproduces this arithmetic exactly.
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

// derefStr renders an optional schema string, empty when absent.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
