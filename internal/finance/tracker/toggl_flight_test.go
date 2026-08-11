package tracker

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestGetCachedSingleFlights: N readers arriving for the same key while a fetch
// is in progress must share that one fetch. Before this, ten concurrent loads
// of the same year fired ten identical year-wide detailed reports at the API
// that is already the slow part.
func TestGetCachedSingleFlights(t *testing.T) {
	tg := &Toggl{}
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex

	fn := func() (any, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release // hold the fetch open until every reader has arrived
		return "value", nil
	}

	const readers = 10
	var wg sync.WaitGroup
	results := make([]any, readers)
	errs := make([]error, readers)
	for i := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = tg.getCached("k", mar(1), mar(31), fn)
		}()
	}

	// Give the goroutines time to pile up on the same key, then let the
	// single leader finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls != 1 {
		t.Errorf("fn called %d times, want 1 — the fetch was not shared", calls)
	}
	for i := range readers {
		if errs[i] != nil {
			t.Errorf("reader %d: %v", i, errs[i])
		}
		if results[i] != "value" {
			t.Errorf("reader %d got %v, want the leader's value", i, results[i])
		}
	}
}

// TestGetCachedSingleFlightSharesFailure: followers must see the leader's
// failure rather than each starting their own doomed fetch.
func TestGetCachedSingleFlightSharesFailure(t *testing.T) {
	tg := &Toggl{}
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex

	fn := func() (any, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return nil, errors.New("boom")
	}

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = tg.getCached("k", mar(1), mar(31), fn)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
	for i, err := range errs {
		if err == nil {
			t.Errorf("reader %d got no error, want the leader's", i)
		}
	}
}

// TestBreakerStopsHammeringAFailingKey pins the pile-up fix: after
// togglBreakerThreshold consecutive failures the key is left alone entirely,
// so a Toggl outage stops turning every page load into a fresh timeout.
func TestBreakerStopsHammeringAFailingKey(t *testing.T) {
	defer withBreakerCooldown(time.Hour)()

	tg := &Toggl{}
	calls := 0
	fn := func() (any, error) { calls++; return nil, errors.New("boom") }

	for i := range togglBreakerThreshold {
		if _, err := tg.getCached("k", mar(1), mar(31), fn); err == nil {
			t.Fatalf("attempt %d: expected an error", i+1)
		}
	}
	if calls != togglBreakerThreshold {
		t.Fatalf("fn called %d times, want %d before the breaker opens", calls, togglBreakerThreshold)
	}

	// Breaker is open now: further reads must not reach fn at all.
	for range 5 {
		if _, err := tg.getCached("k", mar(1), mar(31), fn); err == nil {
			t.Error("expected an error while the breaker is open")
		}
	}
	if calls != togglBreakerThreshold {
		t.Errorf("fn called %d times, want it untouched while the breaker is open", calls)
	}
}

// TestBreakerServesStaleWhileOpen: with a previous value in hand, an open
// breaker must hand that back rather than an error — the whole point is to
// stop calling Toggl, not to stop rendering figures.
func TestBreakerServesStaleWhileOpen(t *testing.T) {
	defer withBreakerCooldown(time.Hour)()

	tg := &Toggl{}
	fail := false
	fn := func() (any, error) {
		if fail {
			return nil, errors.New("boom")
		}
		return "good", nil
	}

	if _, err := tg.getCached("k", mar(1), mar(31), fn); err != nil {
		t.Fatal(err)
	}
	fail = true
	for range togglBreakerThreshold {
		tg.EvictRange(mar(10), mar(20)) // Reload: mark stale, reset the breaker
		if v, err := tg.getCached("k", mar(1), mar(31), fn); err != nil || v != "good" {
			t.Fatalf("got %v/%v, want the stale value and no error", v, err)
		}
	}

	// Now let the breaker actually open, without a Reload clearing it.
	tg.getCached("k", mar(1), mar(31), fn)
	v, err := tg.getCached("k", mar(1), mar(31), fn)
	if err != nil {
		t.Errorf("open breaker with a cached value must not error: %v", err)
	}
	if v != "good" {
		t.Errorf("got %v, want the stale value", v)
	}
}

// TestReloadClearsTheBreaker: Reload means "try again now", including when the
// very first fetch failed and there is no cache entry to hang the reset off.
func TestReloadClearsTheBreaker(t *testing.T) {
	defer withBreakerCooldown(time.Hour)()

	tg := &Toggl{}
	calls := 0
	fn := func() (any, error) { calls++; return nil, errors.New("boom") }

	for range togglBreakerThreshold {
		tg.getCached("k", mar(1), mar(31), fn)
	}
	tg.getCached("k", mar(1), mar(31), fn) // blocked by the open breaker
	if calls != togglBreakerThreshold {
		t.Fatalf("fn called %d times, want %d", calls, togglBreakerThreshold)
	}

	tg.EvictRange(mar(10), mar(20))
	tg.getCached("k", mar(1), mar(31), fn)
	if calls != togglBreakerThreshold+1 {
		t.Errorf("fn called %d times after Reload, want %d — Reload must retry immediately", calls, togglBreakerThreshold+1)
	}
}

// withBreakerCooldown sets the cooldown for one test and returns the restore.
func withBreakerCooldown(d time.Duration) func() {
	prev := togglBreakerCooldown
	togglBreakerCooldown = d
	return func() { togglBreakerCooldown = prev }
}
