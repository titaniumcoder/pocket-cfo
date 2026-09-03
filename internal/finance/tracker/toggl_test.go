package tracker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mar(day int) time.Time {
	return time.Date(2026, time.March, day, 0, 0, 0, 0, time.UTC)
}

func yearRange(year int) (time.Time, time.Time) {
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(1, 0, -1)
}

func yearStatus(h HoursSource, year int) (time.Time, bool) {
	start, end := yearRange(year)
	return h.Status(start, end)
}

func yearPending(h HoursSource, year int) bool {
	start, end := yearRange(year)
	return h.Pending(start, end)
}

func markYearStale(h HoursSource, year int) {
	start, end := yearRange(year)
	h.markStale(start, end, 0)
}

func TestGetCachedCachesForever(t *testing.T) {
	tg := &Toggl{}
	calls := 0
	fn := func(context.Context) (any, error) { calls++; return calls, nil }

	for i := 0; i < 3; i++ {
		v, err := tg.getCached(context.Background(), "k", mar(1), mar(31), fn)
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
	fn := func(context.Context) (any, error) { calls++; return nil, errors.New("boom") }

	if _, err := tg.getCached(context.Background(), "k", mar(1), mar(31), fn); err == nil {
		t.Error("expected error")
	}
	if _, err := tg.getCached(context.Background(), "k", mar(1), mar(31), fn); err == nil {
		t.Error("expected error")
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2 (errors must not be cached)", calls)
	}
}

// Once a value has been fetched, a later failure must degrade to it rather
// than to an error.
func TestGetCachedServesStaleOnRefreshFailure(t *testing.T) {
	tg := &Toggl{}
	fail := false
	calls := 0
	fn := func(context.Context) (any, error) {
		calls++
		if fail {
			return nil, errors.New("boom")
		}
		return "good", nil
	}

	if _, err := tg.getCached(context.Background(), "k", mar(1), mar(31), fn); err != nil {
		t.Fatal(err)
	}
	if at, stale := tg.status("k"); at.IsZero() || stale {
		t.Errorf("after a successful fetch: fetchedAt=%v stale=%v, want a timestamp and not stale", at, stale)
	}

	// Reload marks it stale; the refetch behind it fails.
	tg.EvictRange(mar(10), mar(20))
	fail = true
	v, err := tg.getCached(context.Background(), "k", mar(1), mar(31), fn)
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
	if _, err := tg.getCached(context.Background(), "k", mar(1), mar(31), fn); err != nil {
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
	march := func(context.Context) (any, error) { return "march", nil }
	jan := func(context.Context) (any, error) { return "jan", nil }
	projects := func(context.Context) (any, error) { return "projects", nil }

	tg.getCached(context.Background(), "march", mar(1), mar(31), march)
	tg.getCached(context.Background(), "jan", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), jan)
	tg.getCached(context.Background(), "projects", time.Time{}, time.Time{}, projects) // not range-scoped

	// Evict a range overlapping March only.
	tg.EvictRange(mar(10), mar(20))

	// Eviction marks stale rather than deleting; see EvictRange.
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

func TestStatusFollowsTheMonthsInTheRange(t *testing.T) {
	b := &fakeBackend{detailed: func(int) (string, string, string) { return `[]`, "", "" }}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}
	if at, stale := yearStatus(tg, 2026); !at.IsZero() || stale {
		t.Errorf("uncached year: %v/%v, want zero time and not stale", at, stale)
	}
	if _, err := tg.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	if at, stale := yearStatus(tg, 2026); at.IsZero() || stale {
		t.Errorf("cached year: %v/%v, want a fetch time and not stale", at, stale)
	}
	tg.EvictRange(mar(10), mar(20))
	if _, stale := tg.Status(mar(1), mar(31)); !stale {
		t.Error("March should read stale after a Reload of March")
	}
	if _, stale := tg.Status(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)); stale {
		t.Error("April must not read stale after a Reload of March")
	}
	if _, stale := yearStatus(tg, 2026); !stale {
		t.Error("the year reads stale as soon as one of its months is")
	}
}

func TestMonthKeyIsScopedAndMonthly(t *testing.T) {
	tg := &Toggl{ProjectIDs: "1,2"}
	if got, want := tg.monthKey(monthOf(2026, time.March, time.UTC)), "detailed|1,2|2026-03"; got != want {
		t.Errorf("monthKey = %q, want %q", got, want)
	}
}

func TestReloadOfAMonthRefetchesOnlyThatMonth(t *testing.T) {
	var ranges []string
	b := &fakeBackend{detailedForRange: func(startDate, endDate string) (string, string, string) {
		ranges = append(ranges, startDate+".."+endDate)
		return `[]`, "", ""
	}}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}
	ctx := context.Background()
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	tg.EvictRange(mar(1), mar(31))
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-01-01..2026-12-31", "2026-03-01..2026-03-31"}
	if len(ranges) != len(want) || ranges[0] != want[0] || ranges[1] != want[1] {
		t.Errorf("requested ranges = %v, want %v", ranges, want)
	}
}

func TestStaleMonthsAreFetchedInContiguousRunsNewestFirst(t *testing.T) {
	var ranges []string
	b := &fakeBackend{detailedForRange: func(startDate, endDate string) (string, string, string) {
		ranges = append(ranges, startDate+".."+endDate)
		return `[]`, "", ""
	}}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}
	ctx := context.Background()
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	tg.markStale(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), mar(31), 0)
	tg.markStale(time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), 0)
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-01-01..2026-12-31", "2026-08-01..2026-09-30", "2026-02-01..2026-03-31"}
	if len(ranges) != len(want) || ranges[1] != want[1] || ranges[2] != want[2] {
		t.Errorf("requested ranges = %v, want %v", ranges, want)
	}
}

func TestMarkStaleSparesFreshMonthsAndRates(t *testing.T) {
	b := &fakeBackend{detailed: func(int) (string, string, string) { return `[]`, "", "" }}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}
	if _, err := tg.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	tg.getCached(context.Background(), "rates|workspace", everSince, everUntil, func(context.Context) (any, error) { return "rates", nil })

	tg.markStale(everSince, everUntil, time.Hour)
	if _, stale := yearStatus(tg, 2026); stale {
		t.Error("months fetched a moment ago must not be marked stale by an hour-old cutoff")
	}
	tg.markStale(everSince, everUntil, 0)
	if _, stale := yearStatus(tg, 2026); !stale {
		t.Error("a zero cutoff must mark every month stale")
	}
	if e := tg.cache["rates|workspace"]; e.stale {
		t.Error("markStale must leave the rate timelines alone — only Reload refetches those")
	}
}

func TestSplitMonthsKeepsBoundarySpillInTheNearestMonth(t *testing.T) {
	yd := emptyYearData()
	yd.Months[time.March] = []Aggregate{{ProjectID: 1, Seconds: 3600}}
	yd.Months[time.April] = []Aggregate{{ProjectID: 1, Seconds: 1800}}
	yd.Months[time.June] = []Aggregate{{ProjectID: 1, Seconds: 900}}
	yd.Days["2026-03-02"], yd.Days["2026-04-05"], yd.Days["2026-06-01"] = true, true, true

	run := monthRun{first: monthOf(2026, time.March, time.UTC), last: monthOf(2026, time.April, time.UTC)}
	parts := splitMonths(yd, run.months(time.UTC))

	march, april := parts[run.first], parts[run.last]
	if len(march.Months[time.March]) != 1 || !march.Days["2026-03-02"] || len(march.Months) != 1 {
		t.Errorf("March part = %+v", march)
	}
	if len(april.Months[time.April]) != 1 || len(april.Months[time.June]) != 1 || !april.Days["2026-04-05"] || !april.Days["2026-06-01"] {
		t.Errorf("April part = %+v, want the June spill kept under its own month", april)
	}
	merged := mergeYearData(march, april)
	if merged.Months[time.June][0].Seconds != 900 || len(merged.Days) != 3 {
		t.Errorf("merged = %+v, want nothing lost", merged)
	}
}
