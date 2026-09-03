package tracker

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	snapshotVersion = 1
	versionMarker   = "VERSION"
)

type snapshotFile struct {
	Version int                      `json:"version"`
	Entries map[string]snapshotEntry `json:"entries"`
}

type snapshotEntry struct {
	Kind      entryKind       `json:"kind"`
	Start     time.Time       `json:"start"`
	End       time.Time       `json:"end"`
	FetchedAt time.Time       `json:"fetchedAt"`
	Stale     bool            `json:"stale"`
	Data      json.RawMessage `json:"data"`
}

type SnapshotStatus struct {
	Path    string
	Entries int
	Oldest  time.Time
	Newest  time.Time
}

func (t *Toggl) lock() {
	t.mu.Lock()
	if !t.restored {
		t.restored = true
		t.restoreLocked()
	}
}

func (t *Toggl) snapshotPath() string {
	if t.CacheDir == "" {
		return ""
	}
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, t.backend().cacheScope())
	return filepath.Join(t.CacheDir, name+".json")
}

func persisted(kind entryKind) bool {
	return kind == kindMonth || kind == kindProjects || kind == kindRates
}

func (t *Toggl) restoreLocked() {
	path := t.snapshotPath()
	if path == "" {
		return
	}
	ensureCacheVersion(t.CacheDir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("toggl: cache snapshot %s unreadable, starting cold: %v", path, err)
		return
	}
	var file snapshotFile
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("toggl: cache snapshot %s is not valid JSON, starting cold: %v", path, err)
		return
	}
	if file.Version != snapshotVersion {
		log.Printf("toggl: cache snapshot %s is version %d, this build writes %d — starting cold", path, file.Version, snapshotVersion)
		return
	}
	if t.cache == nil {
		t.cache = map[string]cacheEntry{}
	}
	for key, e := range file.Entries {
		val, err := decodeEntry(e)
		if err != nil {
			log.Printf("toggl: cache snapshot %s: skipping %s: %v", path, key, err)
			continue
		}
		t.cache[key] = cacheEntry{val: val, kind: e.Kind, start: e.Start, end: e.End, fetchedAt: e.FetchedAt, stale: e.Stale}
	}
	log.Printf("toggl: restored %d cache entries from %s", len(file.Entries), path)
}

func ensureCacheVersion(dir string) {
	marker := filepath.Join(dir, versionMarker)
	want := strconv.Itoa(snapshotVersion)
	if raw, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(raw)) == want {
		return
	}
	removed := clearCacheFiles(dir)
	if err := writeAtomically(marker, []byte(want+"\n")); err != nil {
		log.Printf("toggl: cache version marker %s not written: %v", marker, err)
		return
	}
	log.Printf("toggl: cache directory %s is not version %s — removed %d cache file(s) and marked it", dir, want, removed)
}

func clearCacheFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".json.tmp")) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			log.Printf("toggl: stale cache file %s not removed: %v", name, err)
			continue
		}
		removed++
	}
	return removed
}

func decodeEntry(e snapshotEntry) (any, error) {
	switch e.Kind {
	case kindMonth:
		var yd YearData
		if err := json.Unmarshal(e.Data, &yd); err != nil {
			return nil, err
		}
		if yd.Months == nil {
			yd.Months = map[time.Month][]Aggregate{}
		}
		if yd.Days == nil {
			yd.Days = map[string]bool{}
		}
		return &yd, nil
	case kindProjects:
		var projects map[int]Project
		return projects, json.Unmarshal(e.Data, &projects)
	case kindRates:
		var spans []rateSpan
		if err := json.Unmarshal(e.Data, &spans); err != nil {
			return nil, err
		}
		if spans == nil {
			spans = []rateSpan{}
		}
		return spans, nil
	}
	return nil, errors.New("unknown entry kind " + string(e.Kind))
}

func (t *Toggl) persist() {
	path := t.snapshotPath()
	if path == "" {
		return
	}
	t.persistMu.Lock()
	defer t.persistMu.Unlock()

	t.lock()
	file, err := t.snapshotLocked()
	t.mu.Unlock()
	if err != nil {
		log.Printf("toggl: cache snapshot not written: %v", err)
		return
	}
	data, err := json.Marshal(file)
	if err != nil {
		log.Printf("toggl: cache snapshot not written: %v", err)
		return
	}
	if err := writeAtomically(path, data); err != nil {
		log.Printf("toggl: cache snapshot %s not written: %v", path, err)
	}
}

func (t *Toggl) snapshotLocked() (snapshotFile, error) {
	file := snapshotFile{Version: snapshotVersion, Entries: map[string]snapshotEntry{}}
	for key, e := range t.cache {
		if !persisted(e.kind) {
			continue
		}
		data, err := json.Marshal(e.val)
		if err != nil {
			return file, err
		}
		file.Entries[key] = snapshotEntry{Kind: e.kind, Start: e.start, End: e.end, FetchedAt: e.fetchedAt, Stale: e.stale, Data: data}
	}
	return file, nil
}

func writeAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (t *Toggl) removeSnapshotLocked() {
	path := t.snapshotPath()
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("toggl: cache snapshot %s not removed: %v", path, err)
	}
}

func (t *Toggl) Snapshot() SnapshotStatus {
	if t == nil {
		return SnapshotStatus{}
	}
	t.lock()
	defer t.mu.Unlock()
	s := SnapshotStatus{Path: t.snapshotPath()}
	for _, e := range t.cache {
		if !persisted(e.kind) {
			continue
		}
		s.Entries++
		if e.kind != kindMonth {
			continue
		}
		if s.Oldest.IsZero() || e.fetchedAt.Before(s.Oldest) {
			s.Oldest = e.fetchedAt
		}
		if e.fetchedAt.After(s.Newest) {
			s.Newest = e.fetchedAt
		}
	}
	return s
}
