package tracker

import (
	"errors"
	"testing"
	"time"
)

func mar(day int) time.Time {
	return time.Date(2026, time.March, day, 0, 0, 0, 0, time.UTC)
}

func TestGetCachedCachesForever(t *testing.T) {
	tg := &Toggl{}
	calls := 0
	fn := func() (any, error) { calls++; return calls, nil }

	for i := 0; i < 3; i++ {
		v, err := tg.getCached("k", mar(1), mar(31), fn)
		if err != nil {
			t.Fatal(err)
		}
		if v.(int) != 1 {
			t.Errorf("got %v, want 1 (cached value)", v)
		}
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
}

func TestGetCachedDoesNotCacheErrors(t *testing.T) {
	tg := &Toggl{}
	calls := 0
	fn := func() (any, error) { calls++; return nil, errors.New("boom") }

	if _, err := tg.getCached("k", mar(1), mar(31), fn); err == nil {
		t.Error("expected error")
	}
	if _, err := tg.getCached("k", mar(1), mar(31), fn); err == nil {
		t.Error("expected error")
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2 (errors must not be cached)", calls)
	}
}

// TestGetCachedServesStaleOnRefreshFailure is the whole point of the stale
// mechanism: once a value has been fetched successfully, a later failure must
// degrade to that value rather than to an error. A blanked Income panel is a
// worse answer than a figure from an hour ago, given the detailed report times
// out routinely.
func TestGetCachedServesStaleOnRefreshFailure(t *testing.T) {
	tg := &Toggl{}
	fail := false
	calls := 0
	fn := func() (any, error) {
		calls++
		if fail {
			return nil, errors.New("boom")
		}
		return "good", nil
	}

	if _, err := tg.getCached("k", mar(1), mar(31), fn); err != nil {
		t.Fatal(err)
	}
	if at, stale := tg.status("k"); at.IsZero() || stale {
		t.Errorf("after a successful fetch: fetchedAt=%v stale=%v, want a timestamp and not stale", at, stale)
	}

	// Reload marks it stale; the refetch behind it fails.
	tg.EvictRange(mar(10), mar(20))
	fail = true
	v, err := tg.getCached("k", mar(1), mar(31), fn)
	if err != nil {
		t.Fatalf("a failed refresh with a previous value must not error: %v", err)
	}
	if v != "good" {
		t.Errorf("got %v, want the last good value", v)
	}
	if _, stale := tg.status("k"); !stale {
		t.Error("entry should still be stale after a failed refresh")
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2", calls)
	}

	// Recovering replaces the value and clears the stale flag.
	fail = false
	if _, err := tg.getCached("k", mar(1), mar(31), fn); err != nil {
		t.Fatal(err)
	}
	if _, stale := tg.status("k"); stale {
		t.Error("a successful refetch should clear the stale flag")
	}
}

// status is a test-only accessor for an arbitrary key, mirroring YearStatus.
func (t *Toggl) status(key string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.cache[key]
	return e.fetchedAt, e.stale
}

func TestEvictRangeIntersecting(t *testing.T) {
	tg := &Toggl{}
	march := func() (any, error) { return "march", nil }
	jan := func() (any, error) { return "jan", nil }
	projects := func() (any, error) { return "projects", nil }

	tg.getCached("march", mar(1), mar(31), march)
	tg.getCached("jan", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), jan)
	tg.getCached("projects", time.Time{}, time.Time{}, projects) // not range-scoped

	// Evict a range overlapping March only.
	tg.EvictRange(mar(10), mar(20))

	// Eviction marks stale rather than deleting — the value stays as a
	// fallback for a refetch that fails. See EvictRange.
	if e, ok := tg.cache["march"]; !ok || !e.stale {
		t.Errorf("march entry: present=%v stale=%v, want present and stale", ok, e.stale)
	}
	if e, ok := tg.cache["jan"]; !ok || e.stale {
		t.Errorf("january entry: present=%v stale=%v, want present and fresh (no overlap)", ok, e.stale)
	}
	if e, ok := tg.cache["projects"]; !ok || e.stale {
		t.Errorf("projects entry: present=%v stale=%v, want present and fresh (not range-scoped)", ok, e.stale)
	}
}

func TestYearStatus(t *testing.T) {
	tg := &Toggl{}
	if at, stale := tg.YearStatus(2026); !at.IsZero() || stale {
		t.Errorf("uncached year: %v/%v, want zero time and not stale", at, stale)
	}
	tg.getCached(tg.yearKey(2026), mar(1), mar(31), func() (any, error) {
		return &YearData{}, nil
	})
	if at, stale := tg.YearStatus(2026); at.IsZero() || stale {
		t.Errorf("cached year: %v/%v, want a fetch time and not stale", at, stale)
	}
}
