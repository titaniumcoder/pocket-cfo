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

// Accounts reads real bank-account balances (accounts.json) from FS and
// caches them in memory, same convention as Budget — read fresh from a real
// directory (DATA_DIR), never embedded, and evicted by the Reload link.
//
// Unlike Budget, a missing accounts.json is not an error: account balances
// are an optional layer. Without the file the dashboard behaves exactly as
// it did before they existed, so an existing deployment doesn't break by
// upgrading into this feature.
type Accounts struct {
	// FS is where accounts.json is read from. A nil FS means "not
	// configured", same nil-means-disabled convention as Tracker.Toggl.
	FS fs.FS

	mu    sync.Mutex
	cache *accountsResult
}

type accountsResult struct {
	file accountsdata.AccountsFile
	err  error
}

// AccountSnapshot is the combined balance across every account and the
// month it OPENS. Accounts are summed and anchored at the latest as_of
// among them (see the schema's as_of description): balances are read at one
// sitting, and one effective date is what the roll-forward below needs.
//
// OpensMonth is deliberately the month AFTER the as_of date: a balance read
// on 31 July is July's closing figure, so it is August's opening one. July
// itself must not see it.
type AccountSnapshot struct {
	OpensMonth yearMonth
	Cents      int
	AccountRow []AccountRow
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
			// Optional layer — absence is the "no balances configured"
			// case, not a failure.
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

// Evict drops the cached accounts.json, so the Reload link picks up a
// hand-edited balance without a restart — same as Budget.Evict.
func (a *Accounts) Evict() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = nil
}

// snapshotFor combines every account into one balance anchored at the
// latest as_of month among them, shifted forward one month (see
// AccountSnapshot.OpensMonth). ok is false when the file has no account
// with a parseable date, which callers treat as "no balance layer at all".
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

// maxRollForwardMonths caps how far a snapshot is carried. A balance read
// years ago has drifted so far from reality that showing it as today's
// opening figure would be worse than showing nothing — and the cap also
// bounds the per-month work below, which is not free.
const maxRollForwardMonths = 24

// privateOpeningCents is the balance carried forward to the START of viewed:
// the snapshot itself in the month it opens, otherwise the previous month's
// closing figure (opening + net income - private expenses), compounded one
// month at a time. Callers guarantee viewed is at or after snap.OpensMonth.
func (t *Tracker) privateOpeningCents(ctx context.Context, snap AccountSnapshot, viewed yearMonth, now time.Time, rateCents int) (int, error) {
	elapsed := viewed.ordinal() - snap.OpensMonth.ordinal()
	if elapsed > maxRollForwardMonths {
		return 0, fmt.Errorf("accounts: balance is %d months stale (last read before %s) — update accounts.json", elapsed, snap.OpensMonth)
	}

	opening := snap.Cents
	// Each completed month between the snapshot and the viewed month moves
	// the balance; the viewed month itself is not applied here, since this
	// is its OPENING figure — Figures.BalanceCents closes it.
	for m := snap.OpensMonth; m.ordinal() < viewed.ordinal(); m = m.addMonths(1) {
		delta, err := t.monthBalanceDelta(ctx, m, now, rateCents)
		if err != nil {
			return 0, err
		}
		opening += delta
	}
	return opening, nil
}

// monthBalanceDelta is what month m adds to (or takes from) the private
// balance: the net income that lands in m minus the private expenses paid
// in m — the same two figures the Expenses panel shows for m, so browsing
// to that month and reading its Balance reproduces this arithmetic exactly.
func (t *Tracker) monthBalanceDelta(ctx context.Context, m yearMonth, now time.Time, rateCents int) (int, error) {
	bv, err := t.Budget.ForMonth(ctx, m.Year, m.Month, now)
	if err != nil {
		return 0, fmt.Errorf("accounts: budget for %s: %w", m, err)
	}
	fundingStart, fundingEnd := fundingRangeForMonth(m.Year, m.Month)
	pv := t.fundingIncome(ctx, fundingStart, fundingEnd, now, rateCents, float64(bv.CompanyTotalSpentCents)/100, bv.CompanyGroups)
	if pv.Err != "" {
		return 0, fmt.Errorf("accounts: income for %s: %s", m, pv.Err)
	}
	return pv.NetIncomeCents - bv.TotalSpentCents, nil
}

// derefStr renders an optional schema string, empty when absent.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
