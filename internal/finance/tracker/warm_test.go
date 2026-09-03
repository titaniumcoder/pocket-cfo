package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The year's data should be cached before anybody loads a page.
func TestWarmFillsTheCacheWithoutARequest(t *testing.T) {
	trk, _ := fullTrackerWithBackend()
	year := time.Now().In(trk.Loc).Year()

	if at, _ := yearStatus(trk.Toggl, year); !at.IsZero() {
		t.Fatal("nothing should be cached before Warm runs")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go trk.Warm(ctx, time.Hour) // one immediate pass, then idle

	waitFor(t, time.Second, func() bool {
		at, stale := yearStatus(trk.Toggl, year)
		return !at.IsZero() && !stale
	}, "the current year to be warmed into the cache")
}

// Each tick must invalidate and refetch the recent months, or the dashboard
// shows boot-time figures forever.
func TestWarmRefreshesTheHotWindowOnTheTicker(t *testing.T) {
	trk, b := fullTrackerWithBackend()
	var ranges []string
	b.detailedForRange = func(startDate, endDate string) (string, string, string) {
		ranges = append(ranges, startDate+".."+endDate)
		return `[]`, "", ""
	}
	now := time.Now().In(trk.Loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, trk.Loc)
	hotStart := today.AddDate(0, 0, -hotWindowDays)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go trk.Warm(ctx, 20*time.Millisecond)

	var first time.Time
	waitFor(t, time.Second, func() bool {
		at, _ := trk.Toggl.Status(hotStart, today)
		first = at
		return !at.IsZero()
	}, "the first warm pass")

	waitFor(t, 2*time.Second, func() bool {
		at, _ := trk.Toggl.Status(hotStart, today)
		return at.After(first)
	}, "a second warm pass to replace the first fetch")
	cancel()

	if len(ranges) < 2 {
		t.Fatalf("ranges = %v, want a cold pull and at least one hot-window refresh", ranges)
	}
	if want := hotStart.Format("2006-01") + "-01.."; !strings.HasPrefix(ranges[1], want) {
		t.Errorf("second fetch asked for %q, want it to start with the hot window's month %q", ranges[1], want)
	}
	if strings.HasSuffix(ranges[1], "-12-31") && today.Month() != time.December && hotStart.Month() != time.December {
		t.Errorf("second fetch %q re-pulled the whole year", ranges[1])
	}
}

// A nil Toggl must not leave a ticker spinning over nothing.
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

// A request joining the refresher's multi-minute fetch must leave on its own
// deadline, or the warmer holds pages open instead of freeing them.
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

// Cold start: nothing cached, a fetch under way. The request must return
// promptly with a loading state and an auto-refresh.
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

	w := httptest.NewRecorder()
	RenderPage(w, f)
	body := w.Body.String()
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("a pending page must carry the meta refresh")
	}
	if !strings.Contains(body, "Fetching tracked hours from Toggl") {
		t.Error("a pending page must say what it is waiting for")
	}

	// The abandoned fetch must still be running, or the refresh finds nothing.
	if !yearPending(trk.Toggl, year) {
		t.Error("giving up on the wait must not have cancelled the fetch")
	}
}

// withTogglPatience sets the per-request Toggl budget for one test.
func withTogglPatience(d time.Duration) func() {
	prev := togglPatience
	togglPatience = d
	return func() { togglPatience = prev }
}

// waitFor polls cond until it holds or the budget runs out — the loop under
// test has no completion signal of its own.
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
