package tracker

import (
	"context"
	"testing"
	"time"
)

// TestWarmFillsTheCacheWithoutARequest is the point of the refresher: the
// year's data should be sitting in the cache before anybody loads a page, so
// the request never pays for the fetch.
func TestWarmFillsTheCacheWithoutARequest(t *testing.T) {
	trk, _ := fullTrackerWithBackend()
	year := time.Now().In(trk.Loc).Year()

	if at, _ := trk.Toggl.YearStatus(year); !at.IsZero() {
		t.Fatal("nothing should be cached before Warm runs")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go trk.Warm(ctx, time.Hour) // one immediate pass, then idle

	waitFor(t, time.Second, func() bool {
		at, stale := trk.Toggl.YearStatus(year)
		return !at.IsZero() && !stale
	}, "the current year to be warmed into the cache")
}

// TestWarmRefreshesOnTheTicker: a warmed entry must not simply sit there —
// each tick has to invalidate and refetch, or the dashboard would show boot
// time's figures forever.
func TestWarmRefreshesOnTheTicker(t *testing.T) {
	trk, _ := fullTrackerWithBackend()
	year := time.Now().In(trk.Loc).Year()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go trk.Warm(ctx, 20*time.Millisecond)

	var first time.Time
	waitFor(t, time.Second, func() bool {
		at, _ := trk.Toggl.YearStatus(year)
		first = at
		return !at.IsZero()
	}, "the first warm pass")

	waitFor(t, 2*time.Second, func() bool {
		at, _ := trk.Toggl.YearStatus(year)
		return at.After(first)
	}, "a second warm pass to replace the first fetch")
}

// TestWarmWithoutTogglReturnsImmediately: tracked hours are optional, and a
// nil Toggl must not leave a ticker spinning over nothing.
func TestWarmWithoutTogglReturnsImmediately(t *testing.T) {
	trk := &Tracker{Loc: time.UTC}
	done := make(chan struct{})
	go func() {
		trk.Warm(context.Background(), time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Warm should return at once when Toggl is not configured")
	}
}

// TestGetCachedFollowerGivesUpOnContext is what makes the refresher safe to
// have in front of a page request. The refresher's fetch runs on a multi-minute
// deadline; a request that joins it must leave on its own schedule rather than
// inheriting that one, or the warmer would hold pages open instead of freeing
// them.
func TestGetCachedFollowerGivesUpOnContext(t *testing.T) {
	tg := &Toggl{}
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	go func() {
		tg.getCached(context.Background(), "k", mar(1), mar(31), func() (any, error) {
			close(started)
			<-release
			return "value", nil
		})
	}()
	<-started // the leader is registered and fetching

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if _, err := tg.getCached(ctx, "k", mar(1), mar(31), nil); err == nil {
		t.Error("expected the follower's deadline to surface as an error (nothing cached to fall back on)")
	}
	if elapsed := time.Since(begin); elapsed > 500*time.Millisecond {
		t.Errorf("follower waited %s — it should give up at its own deadline, not the leader's", elapsed)
	}
}

// waitFor polls cond until it holds or the budget runs out. Polling rather
// than synchronising because the thing under test is a background loop with no
// completion signal of its own.
func waitFor(t *testing.T, budget time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", budget, what)
}
