package tracker

import (
	"os"
	"slices"
	"time"
)

type CacheStats struct {
	Months, StaleMonths int
	Oldest, Newest      time.Time
	Entries             int
	InFlight            int
	OpenBreakers        int
	Hits, StaleServed   int
	Fetches, Failures   int
	Requests, Retries   int
	LastFetchAt         time.Time
	LastFetchTook       time.Duration
	SnapshotPath        string
	SnapshotBytes       int64
	SnapshotWrittenAt   time.Time
	HeadersSeen         []string
	QuotaHeaders        map[string]string
}

func (t *Toggl) Stats(now time.Time) CacheStats {
	if t == nil {
		return CacheStats{}
	}
	t.lock()
	s := CacheStats{
		Entries:       len(t.cache),
		InFlight:      len(t.inflight),
		Hits:          t.counters.Hits,
		StaleServed:   t.counters.StaleServed,
		Fetches:       t.counters.Fetches,
		Failures:      t.counters.Failures,
		Requests:      t.counters.Requests,
		Retries:       t.counters.Retries,
		LastFetchAt:   t.counters.LastFetchAt,
		LastFetchTook: t.counters.LastFetchTook,
		SnapshotPath:  t.snapshotPath(),
		QuotaHeaders:  map[string]string{},
	}
	for name := range t.headersSeen {
		s.HeadersSeen = append(s.HeadersSeen, name)
	}
	slices.Sort(s.HeadersSeen)
	for name, value := range t.quotaHeaders {
		s.QuotaHeaders[name] = value
	}
	for _, e := range t.cache {
		if e.kind != kindMonth {
			continue
		}
		s.Months++
		if e.stale {
			s.StaleMonths++
		}
		if s.Oldest.IsZero() || e.fetchedAt.Before(s.Oldest) {
			s.Oldest = e.fetchedAt
		}
		if e.fetchedAt.After(s.Newest) {
			s.Newest = e.fetchedAt
		}
	}
	for _, b := range t.breaker {
		if now.Before(b.openUntil) {
			s.OpenBreakers++
		}
	}
	t.mu.Unlock()
	if s.SnapshotPath != "" {
		if fi, err := os.Stat(s.SnapshotPath); err == nil {
			s.SnapshotBytes, s.SnapshotWrittenAt = fi.Size(), fi.ModTime()
		}
	}
	return s
}
