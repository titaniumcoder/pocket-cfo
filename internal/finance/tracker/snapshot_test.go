package tracker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotSurvivesARestartWithoutARequest(t *testing.T) {
	dir := t.TempDir()
	first := focusToggl(&fakeFocus{
		entries:      onePage(entry("2026-03-10", 3600, 1)),
		projects:     `[{"id":1,"name":"Alpha"}]`,
		projectRates: map[int]string{1: rate50},
	}, "")
	first.CacheDir = dir
	ctx := context.Background()
	if _, err := first.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Projects(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "focus_.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot not written at %s: %v", path, err)
	}

	cold := &fakeFocus{failEntries: 500}
	second := focusToggl(cold, "")
	second.CacheDir = dir
	yd, err := second.Year(ctx, 2026)
	if err != nil {
		t.Fatalf("restored year: %v", err)
	}
	if got := yd.Months[time.March]; len(got) != 1 || got[0].Seconds != 3600 || got[0].AmountCents != 5000 {
		t.Errorf("restored March = %+v", got)
	}
	projects, err := second.Projects(ctx)
	if err != nil || projects[1].Name != "Alpha" {
		t.Errorf("restored projects = %+v, %v", projects, err)
	}
	if len(cold.calls) != 0 {
		t.Errorf("the restored client made requests: %v", cold.calls)
	}
	if at, stale := yearStatus(second, 2026); at.IsZero() || stale {
		t.Errorf("restored status = %v/%v, want the original fetch time and fresh", at, stale)
	}
	if _, ok := second.cache["rates|project|1"]; !ok {
		t.Error("the rate timeline was not restored")
	}
	s := second.Snapshot()
	if s.Path != path || s.Entries != 14 || s.Oldest.IsZero() {
		t.Errorf("Snapshot = %+v, want the path, 12 months + projects + one rate timeline", s)
	}
}

func TestSnapshotKeepsStalenessSoAFailedRefreshIsRetriedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBackend{detailed: func(int) (string, string, string) { return `[]`, "", "" }}
	first := &Toggl{WorkspaceID: "ws", HTTP: b.transport(), CacheDir: dir}
	if _, err := first.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	first.EvictRange(mar(1), mar(31))
	b.failDetailed = 500
	if _, err := first.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}

	second := &Toggl{WorkspaceID: "ws", HTTP: b.transport(), CacheDir: dir}
	if _, stale := second.Status(mar(1), mar(31)); !stale {
		t.Error("March was stale when the snapshot was written and must come back stale")
	}
}

func TestSnapshotOfAnotherVersionOrGarbageStartsCold(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":       "not json",
		"other version": `{"version":99,"entries":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "detailed_.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			calls := 0
			b := &fakeBackend{detailed: func(int) (string, string, string) { calls++; return `[]`, "", "" }}
			tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport(), CacheDir: dir}
			if _, err := tg.Year(context.Background(), 2026); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Errorf("made %d requests, want a cold fetch", calls)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "detailed_.json"))
			if err != nil {
				t.Fatal(err)
			}
			var file snapshotFile
			if err := json.Unmarshal(raw, &file); err != nil || file.Version != snapshotVersion {
				t.Errorf("the unusable file was not replaced by a valid snapshot: %v %s", err, raw)
			}
		})
	}
}

func TestSnapshotPathIsScopedPerBackendAndProjectList(t *testing.T) {
	track := &Toggl{ProjectIDs: "1,2", CacheDir: "/var/cache/pocketcfo"}
	if got := track.snapshotPath(); got != "/var/cache/pocketcfo/detailed_1_2.json" {
		t.Errorf("track path = %q", got)
	}
	focus := focusToggl(&fakeFocus{}, "7")
	focus.CacheDir = "/var/cache/pocketcfo"
	if got := focus.snapshotPath(); !strings.HasSuffix(got, "/focus_7.json") {
		t.Errorf("focus path = %q", got)
	}
	if got := (&Toggl{}).snapshotPath(); got != "" {
		t.Errorf("without a dir the path must be empty, got %q", got)
	}
}

func TestAnotherCacheVersionWipesTheDirectoryOnStart(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(versionMarker, "0\n")
	write("detailed_.json", `{"version":1,"entries":{}}`)
	write("focus_1_2.json", `{"version":0,"entries":{}}`)
	write("detailed_.json.tmp", "half written")
	write("notes.txt", "not ours")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("sub", "keep.json"), "not ours either")

	calls := 0
	b := &fakeBackend{detailed: func(int) (string, string, string) { calls++; return `[]`, "", "" }}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport(), CacheDir: dir}
	if _, err := tg.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("made %d requests, want a cold fetch after the wipe", calls)
	}
	for _, gone := range []string{"focus_1_2.json", "detailed_.json.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s survived the version change", gone)
		}
	}
	for _, kept := range []string{"notes.txt", filepath.Join("sub", "keep.json")} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s is not a cache file and must not be touched: %v", kept, err)
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, versionMarker))
	if err != nil || strings.TrimSpace(string(marker)) != "1" {
		t.Errorf("marker = %q, %v; want the current version", marker, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed_.json"))
	if err != nil || !strings.Contains(string(raw), `"version":1`) {
		t.Errorf("a fresh snapshot was not written after the wipe: %v", err)
	}
}

func TestAMissingMarkerTreatsExistingFilesAsAnOldVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "detailed_.json"), []byte(`{"version":1,"entries":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tg := &Toggl{WorkspaceID: "ws", HTTP: (&fakeBackend{}).transport(), CacheDir: dir}
	tg.lock()
	tg.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "detailed_.json")); err == nil {
		t.Error("a file from before the marker existed must be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, versionMarker)); err != nil {
		t.Errorf("marker not written: %v", err)
	}
}

func TestAMatchingMarkerLeavesTheSecondBackendsFileAlone(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBackend{detailed: func(int) (string, string, string) { return `[]`, "", "" }, focus: &fakeFocus{}}
	track := &Toggl{WorkspaceID: "ws", HTTP: b.transport(), CacheDir: dir}
	if _, err := track.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	focus := NewFocus(FocusConfig{Key: "k", OrganizationID: "10", WorkspaceID: "20"}, b.transport())
	focus.CacheDir = dir
	if _, err := focus.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"detailed_.json", "focus_.json", versionMarker} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
}
