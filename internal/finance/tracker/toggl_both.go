package tracker

import (
	"context"
	"log"
	"sync"
	"time"
)

type Combined struct {
	Track *Toggl
	Focus *Toggl

	mu       sync.Mutex
	collided map[int]bool
}

func Both(track, focus *Toggl) *Combined {
	return &Combined{Track: track, Focus: focus, collided: map[int]bool{}}
}

func (c *Combined) Year(ctx context.Context, year int) (*YearData, error) {
	var track, focus *YearData
	var terr, ferr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); track, terr = c.Track.Year(ctx, year) }()
	go func() { defer wg.Done(); focus, ferr = c.Focus.Year(ctx, year) }()
	wg.Wait()
	if terr != nil {
		return nil, terr
	}
	if ferr != nil {
		return nil, ferr
	}
	return mergeYearData(track, focus), nil
}

func (c *Combined) Projects(ctx context.Context) (map[int]Project, error) {
	track, err := c.Track.Projects(ctx)
	if err != nil {
		return nil, err
	}
	focus, err := c.Focus.Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]Project, len(track)+len(focus))
	for id, p := range track {
		out[id] = p
	}
	for id, p := range focus {
		if other, clash := track[id]; clash && other != p {
			c.noteCollision(id, other, p)
		}
		out[id] = p
	}
	return out, nil
}

func (c *Combined) noteCollision(id int, track, focus Project) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.collided[id] {
		return
	}
	c.collided[id] = true
	log.Printf("toggl: project id %d is %q in Toggl Track and %q in Toggl 2.0 — showing the Toggl 2.0 name and adding both projects' hours together", id, track.Name, focus.Name)
}

func (c *Combined) Pending(start, end time.Time) bool {
	return c.Track.Pending(start, end) || c.Focus.Pending(start, end)
}

func (c *Combined) Status(start, end time.Time) (fetchedAt time.Time, stale bool) {
	trackAt, trackStale := c.Track.Status(start, end)
	focusAt, focusStale := c.Focus.Status(start, end)
	if trackAt.IsZero() || focusAt.IsZero() {
		return time.Time{}, false
	}
	if focusAt.Before(trackAt) {
		trackAt = focusAt
	}
	return trackAt, trackStale || focusStale
}

func (c *Combined) EvictRange(start, end time.Time) {
	c.Track.EvictRange(start, end)
	c.Focus.EvictRange(start, end)
}

func (c *Combined) markStale(start, end time.Time, olderThan time.Duration) {
	c.Track.markStale(start, end, olderThan)
	c.Focus.markStale(start, end, olderThan)
}

func (c *Combined) KeyStatus(today time.Time) KeyStatus {
	if s := c.Focus.KeyStatus(today); s.Warning != "" {
		return s
	}
	return c.Track.KeyStatus(today)
}

func (c *Combined) Reset() {
	c.Track.Reset()
	c.Focus.Reset()
}

func (c *Combined) Quota(now time.Time) QuotaStatus {
	focus, track := c.Focus.Quota(now), c.Track.Quota(now)
	switch {
	case focus.Exhausted:
		return focus
	case track.Exhausted:
		return track
	case focus.Remaining < 0:
		return track
	case track.Remaining < 0 || focus.Remaining <= track.Remaining:
		return focus
	}
	return track
}

func (c *Combined) Mode() Mode {
	return ModeBoth
}
