package tracker

import (
	"context"
	"testing"
	"time"
)

func TestStatsCountWhatTheCacheDid(t *testing.T) {
	defer withBreakerCooldown(time.Hour)()
	dir := t.TempDir()
	b := &fakeBackend{detailed: func(int) (string, string, string) { return `[]`, "", "" }, projects: `[]`}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport(), CacheDir: dir}
	ctx := context.Background()
	now := time.Now()

	if s := tg.Stats(now); s.SnapshotPath != tg.snapshotPath() || s.Entries != 0 || s.Fetches != 0 || s.Requests != 0 || len(s.HeadersSeen) != 0 {
		t.Errorf("fresh client Stats = %+v, want only the snapshot path", s)
	}

	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := tg.Projects(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s := tg.Stats(now)
	if s.Months != 12 || s.StaleMonths != 0 || s.Entries != 13 {
		t.Errorf("after a year and projects: %+v", s)
	}
	if s.Fetches != 2 || s.Failures != 0 || s.Requests != 2 || s.Hits != 2 {
		t.Errorf("counters after 2 fetches and 2 cache hits: %+v", s)
	}
	if s.LastFetchAt.IsZero() || s.Oldest.IsZero() || s.SnapshotBytes == 0 {
		t.Errorf("last fetch, month age or snapshot size missing: %+v", s)
	}

	tg.EvictRange(mar(1), mar(31))
	b.failDetailed = 500
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	s = tg.Stats(now)
	if s.StaleMonths != 1 || s.Failures != 1 || s.StaleServed != 0 {
		t.Errorf("after a failed refresh of March: %+v", s)
	}
	if s.Retries != togglAttempts-1 || s.Requests != 2+togglAttempts {
		t.Errorf("a 500 is retried %d times: %+v", togglAttempts-1, s)
	}

	for range togglBreakerThreshold {
		tg.Projects(ctx)
		tg.EvictRange(time.Time{}, time.Time{})
	}
	tg.getCached(ctx, "projects", time.Time{}, time.Time{}, fetchStatus(500))
	if s := tg.Stats(now); s.OpenBreakers == 0 && s.InFlight != 0 {
		t.Errorf("breaker state not reported: %+v", s)
	}

	var none *Toggl
	if s := none.Stats(now); s.SnapshotPath != "" || s.Entries != 0 || s.Requests != 0 {
		t.Errorf("nil Stats = %+v", s)
	}
}

func TestStatsCountStaleServesAndBreakers(t *testing.T) {
	defer withBreakerCooldown(time.Hour)()
	tg := &Toggl{}
	fail := false
	fn := func(context.Context) (any, error) {
		if fail {
			return nil, apiError("toggl", statusResponse(500, "nope"))
		}
		return "good", nil
	}
	ctx := context.Background()
	tg.getCached(ctx, "k", mar(1), mar(31), fn)
	fail = true
	for range togglBreakerThreshold + 2 {
		tg.EvictRange(mar(1), mar(31))
		tg.getCached(ctx, "k", mar(1), mar(31), fn)
	}
	s := tg.Stats(time.Now())
	if s.StaleServed != togglBreakerThreshold+2 || s.Failures != togglBreakerThreshold+2 {
		t.Errorf("stale serves and failures: %+v", s)
	}
	tg.getCached(ctx, "k", mar(1), mar(31), fn)
	tg.getCached(ctx, "k", mar(1), mar(31), fn)
	tg.getCached(ctx, "k", mar(1), mar(31), fn)
	if s := tg.Stats(time.Now()); s.OpenBreakers != 1 || s.StaleServed != togglBreakerThreshold+5 {
		t.Errorf("an open breaker serving stale: %+v", s)
	}
}
