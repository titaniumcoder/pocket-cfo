package tracker

import (
	"context"
	"log"
	"sync"
	"time"
)

type Combined struct {
	Track *Toggl
	Focus *Toggl

	mu       sync.Mutex
	collided map[int]bool
}

func Both(track, focus *Toggl) *Combined {
	return &Combined{Track: track, Focus: focus, collided: map[int]bool{}}
}

func (c *Combined) Year(ctx context.Context, year int) (*YearData, error) {
	var track, focus *YearData
	var terr, ferr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); track, terr = c.Track.Year(ctx, year) }()
	go func() { defer wg.Done(); focus, ferr = c.Focus.Year(ctx, year) }()
	wg.Wait()
	if terr != nil {
		return nil, terr
	}
	if ferr != nil {
		return nil, ferr
	}
	return mergeYearData(track, focus), nil
}

func mergeYearData(a, b *YearData) *YearData {
	type key struct {
		pid, rate int
		currency  string
		month     time.Month
	}
	acc := map[key]*Aggregate{}
	var order []key
	out := &YearData{Months: map[time.Month][]Aggregate{}, Days: map[string]bool{}}
	for _, yd := range []*YearData{a, b} {
		for month, aggs := range yd.Months {
			for _, agg := range aggs {
				k := key{agg.ProjectID, agg.RateCents, agg.Currency, month}
				if acc[k] == nil {
					order = append(order, k)
				}
				acc[k] = addAggregate(acc[k], agg.ProjectID, agg.RateCents, agg.Currency, agg.AmountCents, agg.Seconds)
			}
		}
		for day := range yd.Days {
			out.Days[day] = true
		}
	}
	for _, k := range order {
		out.Months[k.month] = append(out.Months[k.month], *acc[k])
	}
	return out
}

func (c *Combined) Projects(ctx context.Context) (map[int]Project, error) {
	track, err := c.Track.Projects(ctx)
	if err != nil {
		return nil, err
	}
	focus, err := c.Focus.Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]Project, len(track)+len(focus))
	for id, p := range track {
		out[id] = p
	}
	for id, p := range focus {
		if other, clash := track[id]; clash && other != p {
			c.noteCollision(id, other, p)
		}
		out[id] = p
	}
	return out, nil
}

func (c *Combined) noteCollision(id int, track, focus Project) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.collided[id] {
		return
	}
	c.collided[id] = true
	log.Printf("toggl: project id %d is %q in Toggl Track and %q in Toggl 2.0 — showing the Toggl 2.0 name and adding both projects' hours together", id, track.Name, focus.Name)
}

func (c *Combined) YearPending(year int) bool {
	return c.Track.YearPending(year) || c.Focus.YearPending(year)
}

func (c *Combined) YearStatus(year int) (fetchedAt time.Time, stale bool) {
	trackAt, trackStale := c.Track.YearStatus(year)
	focusAt, focusStale := c.Focus.YearStatus(year)
	if trackAt.IsZero() || focusAt.IsZero() {
		return time.Time{}, false
	}
	if focusAt.Before(trackAt) {
		trackAt = focusAt
	}
	return trackAt, trackStale || focusStale
}

func (c *Combined) EvictRange(start, end time.Time) {
	c.Track.EvictRange(start, end)
	c.Focus.EvictRange(start, end)
}

func (c *Combined) markYearStale(year int) {
	c.Track.markYearStale(year)
	c.Focus.markYearStale(year)
}

func (c *Combined) KeyStatus(today time.Time) KeyStatus {
	if s := c.Focus.KeyStatus(today); s.Warning != "" {
		return s
	}
	return c.Track.KeyStatus(today)
}

func (c *Combined) Mode() Mode {
	return ModeBoth
}
