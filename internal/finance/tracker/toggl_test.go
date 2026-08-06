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

	if _, ok := tg.cache["march"]; ok {
		t.Error("march entry should have been evicted")
	}
	if _, ok := tg.cache["jan"]; !ok {
		t.Error("january entry should remain (no overlap)")
	}
	if _, ok := tg.cache["projects"]; !ok {
		t.Error("projects entry should remain (not range-scoped)")
	}
}

func TestYearFetchedAt(t *testing.T) {
	tg := &Toggl{}
	if !tg.YearFetchedAt(2026).IsZero() {
		t.Error("uncached year should report zero time")
	}
	tg.getCached(tg.yearKey(2026), mar(1), mar(31), func() (any, error) {
		return &YearData{}, nil
	})
	if tg.YearFetchedAt(2026).IsZero() {
		t.Error("cached year should report a non-zero fetch time")
	}
}
