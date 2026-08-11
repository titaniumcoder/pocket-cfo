package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
		tg.getCached(context.Background(), "k", mar(1), mar(31), func(context.Context) (any, error) {
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

// TestComputeRendersPendingRatherThanWaiting is the cold-start case: nothing
// cached, a fetch under way, and a reader in front of an empty page. The
// request must come back promptly with a "still loading" state and an
// auto-refresh, instead of holding the response open for the slowest call this
// app makes — which is what made the dashboard feel broken.
func TestComputeRendersPendingRatherThanWaiting(t *testing.T) {
	defer withTogglPatience(50 * time.Millisecond)()

	release := make(chan struct{})
	defer close(release)
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/search/time_entries") {
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return jsonResponse(`[]`, nil), nil
	})
	client := &http.Client{Transport: rt}
	trk := &Tracker{
		Toggl: &Toggl{WorkspaceID: "ws", HTTP: client}, Holidays: &Holidays{HTTP: client},
		HoursPerDay: 8, Loc: time.UTC, RateCents: 7500, RateCurrency: "EUR",
	}

	year := time.Now().In(trk.Loc).Year()
	begin := time.Now()
	f := trk.ComputeMonth(context.Background(), year, time.March)
	elapsed := time.Since(begin)

	if !f.TogglPending {
		t.Error("TogglPending should be set: nothing cached and a fetch is in flight")
	}
	if elapsed > 2*time.Second {
		t.Errorf("ComputeMonth took %s — it should give up at togglPatience, not wait out the fetch", elapsed)
	}

	// The rendered page has to actually ask the browser to come back, or the
	// reader is left staring at a permanently empty panel.
	w := httptest.NewRecorder()
	RenderPage(w, f)
	body := w.Body.String()
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("a pending page must carry the meta refresh")
	}
	if !strings.Contains(body, "Fetching tracked hours from Toggl") {
		t.Error("a pending page must say what it is waiting for")
	}

	// And the abandoned fetch must still be running on the shared context, so
	// the next load finds it — that is what makes the refresh worth doing.
	if !trk.Toggl.YearPending(year) {
		t.Error("giving up on the wait must not have cancelled the fetch")
	}
}

// withTogglPatience sets the per-request Toggl budget for one test.
func withTogglPatience(d time.Duration) func() {
	prev := togglPatience
	togglPatience = d
	return func() { togglPatience = prev }
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
