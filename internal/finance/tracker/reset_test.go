package tracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResetEmptiesTheCacheAndRemovesTheSnapshot(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	b := &fakeBackend{detailed: func(int) (string, string, string) { calls++; return `[]`, "", "" }, projects: `[{"id":1,"name":"Alpha"}]`}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport(), CacheDir: dir}
	ctx := context.Background()
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.Projects(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "detailed_.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot missing before the reset: %v", err)
	}

	tg.Reset()

	if len(tg.cache) != 0 {
		t.Errorf("cache still holds %d entries after Reset", len(tg.cache))
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("snapshot still on disk after Reset: %v", err)
	}
	if at, _ := yearStatus(tg, 2026); !at.IsZero() {
		t.Error("Status still reports a fetch time after Reset")
	}
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("detailed endpoint hit %d times, want a second cold fetch after Reset", calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the fetch after Reset did not write a new snapshot: %v", err)
	}
}

func TestResetForgetsBreakerAndRejectionButKeepsTheQuotaGate(t *testing.T) {
	defer withBreakerCooldown(time.Hour)()
	tg := &Toggl{}
	for range togglBreakerThreshold {
		tg.getCached(context.Background(), "k", mar(1), mar(31), fetchStatus(401))
	}
	tg.quotaGateUntil = time.Now().Add(time.Hour)

	tg.Reset()

	if len(tg.breaker) != 0 || tg.KeyStatus(time.Now()).Rejected {
		t.Error("Reset must clear the breaker and the remembered rejection")
	}
	if !tg.Quota(time.Now()).Exhausted {
		t.Error("Reset must not pretend the hourly quota is back")
	}
}

func TestAFetchFinishingAfterResetDoesNotRepopulate(t *testing.T) {
	tg := &Toggl{}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tg.getCached(context.Background(), "k", mar(1), mar(31), func(context.Context) (any, error) {
			close(started)
			<-release
			return "old", nil
		})
	}()
	<-started
	tg.Reset()
	close(release)
	<-done

	if _, ok := tg.cache["k"]; ok {
		t.Error("a fetch begun before Reset repopulated the cache")
	}
	if len(tg.inflight) != 0 {
		t.Errorf("inflight = %v, want empty", tg.inflight)
	}
}

func TestResetOnNilAndCombinedIsSafe(t *testing.T) {
	var none *Toggl
	none.Reset()
	c := bothOver(&fakeBackend{focus: &fakeFocus{}})
	if _, err := c.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	c.Reset()
	if at, _ := yearStatus(c, 2026); !at.IsZero() {
		t.Error("Reset did not reach both sides")
	}
}
