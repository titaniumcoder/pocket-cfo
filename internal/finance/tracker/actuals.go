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

type Actuals struct {
	FS fs.FS

	mu        sync.Mutex
	cache     map[string]*actualsResult
	justWrote committed
}

func (a *Actuals) Publish(monthKey string, body []byte) {
	if a == nil {
		return
	}
	a.justWrote.publish(monthKey, body)
	a.mu.Lock()
	delete(a.cache, monthKey)
	a.mu.Unlock()
}

type actualsResult struct {
	file    actualsdata.ActualsFile
	present bool
	err     error
}

func (a *Actuals) Configured() bool { return a != nil && a.FS != nil }

type ActualsView struct {
	Present    bool
	Complete   bool
	ByCategory map[string]int
	TotalCents int

	UntrackedCents int
	UntrackedCount int

	// CrossedCents is the month's marked movements summed with their own
	// signs, so money out of the company and money back into it need no
	// branch: a draw of +5000 settles 5000 of what is owed, a contribution of
	// -500 adds 500 to it.
	CrossedCents int

	// CompanyCashOutCents is what those same lines did to the company's bank,
	// which is a different question and a different set: a salary transfer
	// settles the loan but is already counted as gross salary, and the two
	// taxes leave the bank without ever reaching the owner.
	CompanyCashOutCents int

	ByMovementRow []MovementTotal

	// DoubleMarked is the one double count the sign rule cannot catch: two
	// company-side lines marked for what is really one transfer. It is a note
	// rather than a refusal, because two genuine draws of the same amount on
	// one day are a real thing and failing the month would take it off the
	// dashboard entirely.
	DoubleMarked bool
}

type MovementTotal struct {
	Movement actualsdata.Movement
	Cents    int
}

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

func (a *Actuals) ForYear(ctx context.Context, year int, start time.Time) (ActualsView, error) {
	out := ActualsView{ByCategory: map[string]int{}}
	months := 0
	complete := 0
	first, last := yearMonthRange(year, floorOf(start))
	want := int(last-first) + 1
	for m := first; m <= last; m++ {
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
	out.Complete = months == want && complete == want
	return out, nil
}

func (a *Actuals) ChargedMonths(ctx context.Context, year int, start time.Time) (map[string][]time.Month, error) {
	if a == nil || a.FS == nil {
		return nil, nil
	}
	out := map[string][]time.Month{}
	first, last := yearMonthRange(year, floorOf(start))
	for m := first; m <= last; m++ {
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

func (a *Actuals) UntrackedMonths(ctx context.Context, year int, start time.Time) (map[time.Month]int, error) {
	if a == nil || a.FS == nil {
		return nil, nil
	}
	out := map[time.Month]int{}
	first, last := yearMonthRange(year, floorOf(start))
	for m := first; m <= last; m++ {
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

func (a *Actuals) TransactionsForMonth(ctx context.Context, year int, month time.Month) (actualsdata.ActualsFile, bool, error) {
	res := a.month(ctx, year, month)
	return res.file, res.present, res.err
}

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
	key := monthKey(year, month)
	content, err := fs.ReadFile(a.FS, path)
	if body, ok := a.justWrote.supersedes(key, content, err); ok {
		content, err = body, nil
	}
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
	if err := actualsdata.ValidateActuals(af, monthKey(year, month), nil); err != nil {
		return actualsResult{err: fmt.Errorf("actuals: %s: %w", path, err)}
	}
	log.Printf("actuals: %s — loaded %d transaction(s)", path, len(af.Transactions))
	return actualsResult{file: af, present: true}
}

func viewOf(af actualsdata.ActualsFile, year int, month time.Month) ActualsView {
	v := ActualsView{Present: true, ByCategory: map[string]int{}}
	byMovement := map[actualsdata.Movement]int{}
	markedOnce := map[string]bool{}
	for _, tx := range af.Transactions {
		untracked := false
		for _, part := range actualsdata.PartsOf(tx) {
			if part.Movement != "" {
				byMovement[part.Movement] += eurToCents(part.Amount)
				if part.Crossed() {
					v.CrossedCents += eurToCents(part.Amount)
				}
				if part.MovedCompanyCash() {
					v.CompanyCashOutCents += eurToCents(part.Amount)
				}
				key := fmt.Sprintf("%s|%s|%d", tx.Date, part.Movement, eurToCents(part.Amount))
				if markedOnce[key] {
					v.DoubleMarked = true
				}
				markedOnce[key] = true
			}
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
		if untracked {
			v.UntrackedCount++
		}
	}
	v.ByMovementRow = movementTotals(byMovement)
	v.Complete = coverageComplete(af, year, month)
	return v
}

// movementTotals keeps the schema's own order rather than a map's, so the
// page does not reshuffle itself between two reads of the same file.
func movementTotals(byMovement map[actualsdata.Movement]int) []MovementTotal {
	if len(byMovement) == 0 {
		return nil
	}
	out := make([]MovementTotal, 0, len(byMovement))
	for _, m := range []actualsdata.Movement{
		actualsdata.MovementSalaryTransfer,
		actualsdata.MovementOwnerDraw,
		actualsdata.MovementDividendPayout,
		actualsdata.MovementOwnerContribution,
		actualsdata.MovementCorporateTax,
		actualsdata.MovementDividendTax,
	} {
		if cents, ok := byMovement[m]; ok {
			out = append(out, MovementTotal{Movement: m, Cents: cents})
		}
	}
	return out
}

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

	reached := first.AddDate(0, 0, -1)
	for _, s := range spans {
		if s.from.After(reached.AddDate(0, 0, 1)) {
			return false
		}
		if s.to.After(reached) {
			reached = s.to
		}
	}
	return !reached.Before(last)
}
