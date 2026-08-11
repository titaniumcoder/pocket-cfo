package tracker

import (
	"context"
	"log"
	"time"
)

const (
	DefaultWarmInterval = 15 * time.Minute

	// warmTimeout bounds one background refresh — far longer than a page would
	// tolerate, which is the point of doing it off the request path.
	warmTimeout = 3 * time.Minute

	// pendingRefresh is the meta-refresh delay, in seconds, on a page rendered
	// before Toggl answered. A string because it is concatenated into the
	// template.
	pendingRefresh = "5"
)

// togglPatience is how long a page request waits on Toggl before rendering
// without it. Short, because giving up no longer abandons the fetch (see
// Toggl.getCached) — the cost is one auto-refresh, not a lost result.
// A variable only so tests can shrink it.
var togglPatience = 5 * time.Second

// waitBudget caps how long a caller waits on Toggl, independently of the rest
// of its budget. Every Toggl read a page request can reach must go through it;
// one call left on the raw request context waits out the whole fetch and undoes
// the short budget everywhere else.
func waitBudget(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, togglPatience)
}

// Warm keeps the current year's Toggl data hot so page requests serve from
// cache. It refreshes once immediately, then every `every`, until ctx is
// cancelled. A nil Toggl (tracked hours disabled) returns at once.
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

// warmOnce refreshes the current year and the project list, logging and
// dropping failures — getCached has already recorded them against the breaker.
func (t *Tracker) warmOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, warmTimeout)
	defer cancel()

	year := time.Now().In(t.location()).Year()
	// Invalidate first, or a fresh entry is served back and this is a no-op.
	// markStale, not EvictRange: a scheduled refresh must not clear the breaker.
	t.Toggl.markStale(t.Toggl.yearKey(year))
	if _, err := t.Toggl.Year(ctx, year); err != nil {
		log.Printf("toggl: background refresh of %d failed: %v", year, err)
	}
	// Not invalidated: project names never change, so this is a cache hit after
	// the first success. Kept in the loop so a failed first fetch heals here
	// rather than waiting for a page load.
	if _, err := t.Toggl.Projects(ctx); err != nil {
		log.Printf("toggl: background refresh of projects failed: %v", err)
	}
}

// location defaults to UTC so a zero-value Tracker doesn't panic on a nil Loc.
func (t *Tracker) location() *time.Location {
	if t.Loc == nil {
		return time.UTC
	}
	return t.Loc
}
