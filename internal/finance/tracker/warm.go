package tracker

import (
	"context"
	"log"
	"time"
)

const (
	// DefaultWarmInterval is how often the background refresher re-reads the
	// current year. Toggl entries are hand-logged over a working day, so
	// anything finer buys accuracy nobody can perceive at the cost of an API
	// this app is already trying to lean on less.
	DefaultWarmInterval = 15 * time.Minute

	// warmTimeout bounds one background refresh. Far longer than any page
	// request would tolerate, which is the point: the refresher is the only
	// caller that can afford to wait out a slow year-wide detailed report,
	// and it does so off the request path where nobody is watching a
	// spinner.
	warmTimeout = 3 * time.Minute
)

// Warm keeps the current calendar year's Toggl data hot in the background, so
// page requests serve from cache instead of each paying for the fetch
// themselves. It refreshes once immediately, then every `every`, and returns
// when ctx is cancelled.
//
// This is what takes Toggl off the request path. A request that arrives while
// a refresh is in flight joins it through getCached's single-flight and waits
// only as long as its own deadline allows (see getCached); the refresh carries
// on regardless, so the next request finds the answer waiting. Combined with
// serving stale data on failure, the dashboard stops depending on Toggl being
// reachable at the moment somebody loads it.
//
// A nil Toggl (tracked hours disabled by config) returns immediately rather
// than spinning a pointless ticker.
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

// warmOnce refreshes the current year and the project list. Failures are
// logged and dropped: the refresher's job is to keep the cache warm when it
// can, and getCached has already recorded the failure against the circuit
// breaker for everyone else's benefit.
func (t *Tracker) warmOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, warmTimeout)
	defer cancel()

	year := time.Now().In(t.location()).Year()
	// Invalidate before reading, or a still-fresh entry would be served back
	// and the refresh would be a no-op. markStale rather than EvictRange:
	// this must not clear a circuit breaker (see markStale).
	t.Toggl.markStale(t.Toggl.yearKey(year))
	if _, err := t.Toggl.Year(ctx, year); err != nil {
		log.Printf("toggl: background refresh of %d failed: %v", year, err)
	}
	// Not marked stale first: project names effectively never change, so this
	// is a cache hit after the first success. It stays in the loop so a failed
	// first fetch heals on the next tick instead of waiting for a page load.
	if _, err := t.Toggl.Projects(ctx); err != nil {
		log.Printf("toggl: background refresh of projects failed: %v", err)
	}
}

// location is Loc, defaulting to UTC so a zero-value Tracker in a test doesn't
// panic on the nil *time.Location.
func (t *Tracker) location() *time.Location {
	if t.Loc == nil {
		return time.UTC
	}
	return t.Loc
}
