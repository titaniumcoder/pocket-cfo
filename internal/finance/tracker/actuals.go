package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
)

func actualsPath(year int, month time.Month) string {
	return fmt.Sprintf("actuals/%04d-%02d.json", year, int(month))
}

// Actuals reads data/actuals/YYYY-MM.json — what was *actually* spent, as
// reconciled from bank statements — and caches each month until evicted.
//
// Unlike Budget, a missing file is not an error: most months simply haven't
// been reconciled, and that must render as "nothing to show" rather than as a
// failure. Same optional-layer contract as Accounts.
//
// Nothing here feeds any arithmetic. Balance, Available to spend, Total
// private expenses and the account roll-forward all stay planned-based; these
// figures are shown beside the plan and never folded into it.
type Actuals struct {
	FS fs.FS // A nil FS means not configured, as with Accounts.

	mu    sync.Mutex
	cache map[string]*actualsResult // keyed "2026-08"
}

type actualsResult struct {
	file    actualsdata.ActualsFile
	present bool
	err     error
}

// ActualsView is one period's recorded spending, ready to display.
type ActualsView struct {
	Present    bool           // false: no file for this period — render nothing at all
	Complete   bool           // coverage reaches month end for every account it names
	ByCategory map[string]int // budget category id -> cents; ignored lines excluded
	TotalCents int
	Note       string // the coverage caveat; empty when Complete
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

// ForYear sums whichever months of the year have been reconciled. Complete
// requires all twelve present and individually complete, so a partly-imported
// year never reads as a finished one.
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
	}
	if months == 0 {
		return ActualsView{}, nil
	}
	out.Present = true
	out.Complete = months == 12 && complete == 12
	if !out.Complete {
		out.Note = fmt.Sprintf("Reconciled for %d of 12 months. Anything outside those months isn't in these figures.", complete)
	}
	return out, nil
}

// ChargedMonths reports, per budget category id, which months of the year it
// was actually charged in. It exists for the mistimed-one-off check, which
// has to look outside the month being viewed: a cost budgeted for August and
// paid in July is invisible from either month alone.
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

// TransactionsForMonth returns the month's raw document, for the admin-only
// spending page. Deliberately separate from ForMonth: descriptions must never
// reach the dashboard's Figures, so the only way to get them is to ask.
func (a *Actuals) TransactionsForMonth(ctx context.Context, year int, month time.Month) (actualsdata.ActualsFile, bool, error) {
	res := a.month(ctx, year, month)
	return res.file, res.present, res.err
}

// Evict drops every cached month, so the existing Reload link picks up a
// fresh data-repo commit.
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
			// Optional layer: an unreconciled month is absence, not failure.
			return actualsResult{}
		}
		return actualsResult{err: fmt.Errorf("actuals: reading %s: %w", path, err)}
	}
	var af actualsdata.ActualsFile
	if err := json.Unmarshal(content, &af); err != nil {
		return actualsResult{err: fmt.Errorf("actuals: parse %s: %w", path, err)}
	}
	// Structural rules only. The category cross-check needs budget.json,
	// which this layer has no business loading — an id that no longer
	// resolves is bucketed as unmatched rather than blanking the page.
	// pocket-cfo-ctl validate is where that is caught properly.
	if err := actualsdata.ValidateActuals(af, monthKey(year, month), nil); err != nil {
		return actualsResult{err: fmt.Errorf("actuals: %s: %w", path, err)}
	}
	log.Printf("actuals: %s — loaded %d transaction(s)", path, len(af.Transactions))
	return actualsResult{file: af, present: true}
}

// viewOf reduces one month's document to the figures the dashboard needs.
// Ignored lines are excluded everywhere: they exist so the file reconciles to
// the whole statement, not so they can be counted.
func viewOf(af actualsdata.ActualsFile, year int, month time.Month) ActualsView {
	v := ActualsView{Present: true, ByCategory: map[string]int{}}
	for _, tx := range af.Transactions {
		if tx.Category == nil || *tx.Category == "" {
			continue
		}
		cents := eurToCents(tx.Amount)
		v.ByCategory[*tx.Category] += cents
		v.TotalCents += cents
	}
	v.Complete = coverageComplete(af, year, month)
	if !v.Complete {
		v.Note = coverageNote(af)
	}
	return v
}

// coverageComplete reports whether every account the file names was read from
// the first of the month to the last, merging that account's ranges first so
// two weekly imports that meet still count as continuous.
//
// It cannot know about an account that was never imported at all — that is
// Hermes' job, and it is why the note names the accounts that *were* read.
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

// coverageNote is the one-line caveat shown when a month is only partly read.
// A fully reconciled month gets no note: there is nothing to warn about, and
// the colour of the figures already carries it.
func coverageNote(af actualsdata.ActualsFile) string {
	earliest := ""
	var accounts []string
	for _, c := range af.Coverage {
		accounts = append(accounts, c.Account)
		if earliest == "" || c.To < earliest {
			earliest = c.To
		}
	}
	sort.Strings(accounts)
	accounts = dedupe(accounts)

	through := earliest
	if d, err := time.Parse("2006-01-02", earliest); err == nil {
		through = d.Format("2 January 2006")
	}
	return fmt.Sprintf("Reconciled through %s · %s. Anything spent since isn't in these figures.",
		through, strings.Join(accounts, ", "))
}

func dedupe(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
