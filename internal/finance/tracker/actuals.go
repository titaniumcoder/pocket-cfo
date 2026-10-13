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

func (a *Actuals) Unpublish(monthKey string) {
	if a == nil {
		return
	}
	a.justWrote.forget(monthKey)
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
	ByCategory map[string]int
	TotalCents int

	UntrackedCents int
	UntrackedCount int
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
	first, last := yearMonthRange(year, floorOf(start))
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
		if untracked {
			v.UntrackedCount++
		}
	}
	return v
}
