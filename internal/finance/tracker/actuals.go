package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
)

func actualsPath(year int, month time.Month) string {
	return fmt.Sprintf("actuals/%04d-%02d.json", year, int(month))
}

// Actuals reads data/actuals/YYYY-MM.json and caches each month until
// evicted. Unlike Budget, a missing file is not an error: an unreconciled
// month renders as nothing at all, the same optional-layer contract Accounts
// has.
type Actuals struct {
	FS fs.FS // nil means not configured

	mu    sync.Mutex
	cache map[string]*actualsResult // keyed "2026-08"
}

type actualsResult struct {
	file    actualsdata.ActualsFile
	present bool
	err     error
}

// Configured reports whether an actuals directory was wired up at all. The
// menu asks before offering the spending page: a month with no file is worth
// showing (it says so), a deployment with no directory is not.
func (a *Actuals) Configured() bool { return a != nil && a.FS != nil }

// ActualsView is one period's recorded spending, ready to display.
type ActualsView struct {
	Present    bool
	Complete   bool           // coverage reaches month end for every account it names
	ByCategory map[string]int // category id -> cents; ignored and untracked excluded
	TotalCents int

	// UntrackedCents is money that left the account and belongs to no category
	// yet. Never in ByCategory or TotalCents: it has been spent, but against
	// nothing, so putting it in a total that gets compared to a plan would
	// misstate the spending and the plan at once. UntrackedCount is lines, not
	// parts.
	UntrackedCents int
	UntrackedCount int
}

// ForMonth returns the recorded spending for one month.
func (a *Actuals) ForMonth(ctx context.Context, year int, month time.Month) (ActualsView, error) {
	res := a.month(ctx, year, month)
	if res.err != nil {
		return ActualsView{}, res.err
	}
	if !res.present {
		return ActualsView{}, nil
	}
	return viewOf(res.file, year, month), nil
}

// ForYear sums whichever months have been reconciled. Complete requires all
// twelve, so a partly-imported year never reads as a finished one.
func (a *Actuals) ForYear(ctx context.Context, year int) (ActualsView, error) {
	out := ActualsView{ByCategory: map[string]int{}}
	months := 0
	complete := 0
	for m := time.January; m <= time.December; m++ {
		res := a.month(ctx, year, m)
		if res.err != nil {
			return ActualsView{}, res.err
		}
		if !res.present {
			continue
		}
		months++
		mv := viewOf(res.file, year, m)
		if mv.Complete {
			complete++
		}
		for id, cents := range mv.ByCategory {
			out.ByCategory[id] += cents
		}
		out.TotalCents += mv.TotalCents
		out.UntrackedCents += mv.UntrackedCents
		out.UntrackedCount += mv.UntrackedCount
	}
	if months == 0 {
		return ActualsView{}, nil
	}
	out.Present = true
	out.Complete = months == 12 && complete == 12
	return out, nil
}

// ChargedMonths reports which months each category was charged in. The
// mistimed check needs it: a cost budgeted for August and paid in July is
// invisible from either month alone.
func (a *Actuals) ChargedMonths(ctx context.Context, year int) (map[string][]time.Month, error) {
	if a == nil || a.FS == nil {
		return nil, nil
	}
	out := map[string][]time.Month{}
	for m := time.January; m <= time.December; m++ {
		res := a.month(ctx, year, m)
		if res.err != nil {
			return nil, res.err
		}
		if !res.present {
			continue
		}
		seen := map[string]bool{}
		for _, tx := range res.file.Transactions {
			if tx.Category == nil || *tx.Category == "" || seen[*tx.Category] {
				continue
			}
			seen[*tx.Category] = true
			out[*tx.Category] = append(out[*tx.Category], m)
		}
	}
	return out, nil
}

// UntrackedMonths reports how much each month of a year still has undecided,
// omitting the months with none. It backs the marker on the month picker, so
// unfinished business is visible from any month rather than only from the one
// it is sitting in — which is the whole reason parking a line under untracked
// is allowed.
//
// Months are cached, so after the first read this is a walk over memory.
func (a *Actuals) UntrackedMonths(ctx context.Context, year int) (map[time.Month]int, error) {
	if a == nil || a.FS == nil {
		return nil, nil
	}
	out := map[time.Month]int{}
	for m := time.January; m <= time.December; m++ {
		res := a.month(ctx, year, m)
		if res.err != nil {
			return nil, res.err
		}
		if !res.present {
			continue
		}
		if v := viewOf(res.file, year, m); v.UntrackedCents != 0 {
			out[m] = v.UntrackedCents
		}
	}
	return out, nil
}

// TransactionsForMonth returns the raw document, for the admin-only spending
// page. Separate from ForMonth so descriptions never reach Figures.
func (a *Actuals) TransactionsForMonth(ctx context.Context, year int, month time.Month) (actualsdata.ActualsFile, bool, error) {
	res := a.month(ctx, year, month)
	return res.file, res.present, res.err
}

// Evict drops every cached month.
func (a *Actuals) Evict() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = nil
}

func (a *Actuals) month(ctx context.Context, year int, month time.Month) actualsResult {
	if a == nil || a.FS == nil {
		return actualsResult{}
	}
	key := monthKey(year, month)

	a.mu.Lock()
	if cached, ok := a.cache[key]; ok {
		a.mu.Unlock()
		return *cached
	}
	a.mu.Unlock()

	res := a.fetch(year, month)

	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[string]*actualsResult{}
	}
	a.cache[key] = &res
	a.mu.Unlock()
	return res
}

func (a *Actuals) fetch(year int, month time.Month) actualsResult {
	path := actualsPath(year, month)
	content, err := fs.ReadFile(a.FS, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return actualsResult{}
		}
		return actualsResult{err: fmt.Errorf("actuals: reading %s: %w", path, err)}
	}
	var af actualsdata.ActualsFile
	if err := json.Unmarshal(content, &af); err != nil {
		return actualsResult{err: fmt.Errorf("actuals: parse %s: %w", path, err)}
	}
	// Structural rules only: the category cross-check needs budget.json, and
	// an id that stopped resolving must degrade to an unmatched bucket rather
	// than blank the page. pocket-cfo-ctl validate does it properly.
	if err := actualsdata.ValidateActuals(af, monthKey(year, month), nil); err != nil {
		return actualsResult{err: fmt.Errorf("actuals: %s: %w", path, err)}
	}
	log.Printf("actuals: %s — loaded %d transaction(s)", path, len(af.Transactions))
	return actualsResult{file: af, present: true}
}

// viewOf reduces one month to the figures the dashboard needs. Ignored lines
// are excluded everywhere, and so is untracked cash — it is counted separately
// so a month can say how much is still undecided without that money reaching
// any total it would misstate.
func viewOf(af actualsdata.ActualsFile, year int, month time.Month) ActualsView {
	v := ActualsView{Present: true, ByCategory: map[string]int{}}
	for _, tx := range af.Transactions {
		untracked := false
		for _, part := range actualsdata.PartsOf(tx) {
			if part.Untracked != "" {
				v.UntrackedCents += eurToCents(part.Amount)
				untracked = true
			}
			if part.Category == "" {
				continue
			}
			cents := eurToCents(part.Amount)
			v.ByCategory[part.Category] += cents
			v.TotalCents += cents
		}
		// Counted per line, like the reconciliation's ignored count: a split
		// with one undecided part is one line to come back to, not two.
		if untracked {
			v.UntrackedCount++
		}
	}
	v.Complete = coverageComplete(af, year, month)
	return v
}

// coverageComplete reports whether every account named was read from the
// first of the month to the last, merging each account's ranges so two weekly
// imports that meet count as continuous. It cannot know about an account
// never imported at all, which is why the note names those that were.
func coverageComplete(af actualsdata.ActualsFile, year int, month time.Month) bool {
	if len(af.Coverage) == 0 {
		return false
	}
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)

	byAccount := map[string][]actualsdata.Coverage{}
	for _, c := range af.Coverage {
		byAccount[c.Account] = append(byAccount[c.Account], c)
	}
	for _, ranges := range byAccount {
		if !spansMonth(ranges, first, last) {
			return false
		}
	}
	return true
}

func spansMonth(ranges []actualsdata.Coverage, first, last time.Time) bool {
	type span struct{ from, to time.Time }
	var spans []span
	for _, r := range ranges {
		from, err1 := time.Parse("2006-01-02", r.From)
		to, err2 := time.Parse("2006-01-02", r.To)
		if err1 != nil || err2 != nil {
			return false
		}
		spans = append(spans, span{from, to})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from.Before(spans[j].from) })

	reached := first.AddDate(0, 0, -1) // the day before the month starts
	for _, s := range spans {
		if s.from.After(reached.AddDate(0, 0, 1)) {
			return false // a gap
		}
		if s.to.After(reached) {
			reached = s.to
		}
	}
	return !reached.Before(last)
}
