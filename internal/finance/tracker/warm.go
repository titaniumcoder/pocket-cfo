package tracker

import (
	"context"
	"log"
	"time"
)

const (
	DefaultWarmInterval = 15 * time.Minute
	hotWindowDays       = 45
	fullRefreshInterval = 24 * time.Hour

	warmTimeout = 3 * time.Minute

	pendingRefresh = "5"
)

var togglPatience = 5 * time.Second

func waitBudget(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, togglPatience)
}

func (t *Tracker) Warm(ctx context.Context, every time.Duration) {
	if t.Toggl == nil {
		return
	}
	if every <= 0 {
		every = DefaultWarmInterval
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		t.warmOnce(ctx, every)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (t *Tracker) warmOnce(parent context.Context, every time.Duration) {
	ctx, cancel := context.WithTimeout(parent, warmTimeout)
	defer cancel()

	now := time.Now().In(t.location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, t.location())
	hotStart := today.AddDate(0, 0, -hotWindowDays)
	t.Toggl.markStale(everSince, everUntil, fullRefreshInterval)
	t.Toggl.markStale(hotStart, today, every/2)
	for year := hotStart.Year(); year <= today.Year(); year++ {
		if _, err := t.Toggl.Year(ctx, year); err != nil {
			log.Printf("toggl: background refresh of %d failed: %v", year, err)
		}
	}
	if _, err := t.Toggl.Projects(ctx); err != nil {
		log.Printf("toggl: background refresh of projects failed: %v", err)
	}
}

func (t *Tracker) location() *time.Location {
	if t.Loc == nil {
		return time.UTC
	}
	return t.Loc
}
