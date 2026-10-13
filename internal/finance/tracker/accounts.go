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

// newestReadingBefore is the whole rule a series of readings follows: a
// reading closes its month and opens the next, the newest one before the month
// being looked at wins, and a month before every reading has nothing in force
// — which is not the same as zero.
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

type openings struct {
	PrivateCents int
}

func (a *Accounts) Snapshot(ctx context.Context, viewed yearMonth) (AccountSnapshot, bool, error) {
	af, err := a.File(ctx)
	if err != nil {
		return AccountSnapshot{}, false, err
	}
	snap, ok := snapshotFor(af, viewed)
	return snap, ok, nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
