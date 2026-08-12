package tracker

import (
	"context"
	"log"
	"time"
)

const (
	DefaultWarmInterval = 15 * time.Minute

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
		t.warmOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (t *Tracker) warmOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, warmTimeout)
	defer cancel()

	year := time.Now().In(t.location()).Year()
	t.Toggl.markStale(t.Toggl.yearKey(year))
	if _, err := t.Toggl.Year(ctx, year); err != nil {
		log.Printf("toggl: background refresh of %d failed: %v", year, err)
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
