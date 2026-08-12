package tracker

import (
	"context"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

type PlannedCategory struct {
	ID           string
	Group        string
	Name         string
	Kind         string
	PlannedCents int
	Overridden   bool
	Date         string
}

func (b *Budget) PlannedForMonth(ctx context.Context, year int, month time.Month) ([]PlannedCategory, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return nil, err
	}
	return plannedFrom(bf, monthKey(year, month)), nil
}

func (b *Budget) PlannedByMonth(ctx context.Context, year int) (map[string][]PlannedCategory, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]PlannedCategory, 12)
	for m := time.January; m <= time.December; m++ {
		key := monthKey(year, m)
		out[key] = plannedFrom(bf, key)
	}
	return out, nil
}

func (b *Budget) CategoryIndex(ctx context.Context) (map[string]PlannedCategory, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]PlannedCategory)
	for _, g := range bf.Groups {
		for _, c := range g.Categories {
			out[c.Id] = PlannedCategory{ID: c.Id, Group: g.Name, Name: c.Name, Kind: string(g.Kind), Date: derefStr(c.Date)}
		}
	}
	return out, nil
}

func plannedFrom(bf budgetdata.BudgetFile, key string) []PlannedCategory {
	var out []PlannedCategory
	for _, g := range bf.Groups {
		for _, c := range g.Categories {
			_, overridden := overrideFor(c, key)
			out = append(out, PlannedCategory{
				ID:           c.Id,
				Group:        g.Name,
				Name:         c.Name,
				Kind:         string(g.Kind),
				PlannedCents: categoryCents(c, key, false),
				Overridden:   overridden,
				Date:         derefStr(c.Date),
			})
		}
	}
	return out
}
