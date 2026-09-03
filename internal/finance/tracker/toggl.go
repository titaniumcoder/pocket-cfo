package tracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Toggl struct {
	Token       string
	WorkspaceID string
	ProjectIDs  string
	HTTP        *http.Client

	api          togglAPI
	keyExpiresAt time.Time

	mu         sync.Mutex
	cache      map[string]cacheEntry
	inflight   map[string]*fetchCall
	breaker    map[string]breakerState
	rejectedAt time.Time
	rejection  string
}

type HoursSource interface {
	Year(ctx context.Context, year int) (*YearData, error)
	Projects(ctx context.Context) (map[int]Project, error)
	YearPending(year int) bool
	YearStatus(year int) (fetchedAt time.Time, stale bool)
	EvictRange(start, end time.Time)
	markYearStale(year int)
	KeyStatus(today time.Time) KeyStatus
	Mode() Mode
}

type Mode string

const (
	ModeOff   Mode = "disabled"
	ModeTrack Mode = "Toggl Track"
	ModeFocus Mode = "Toggl 2.0"
	ModeBoth  Mode = "Toggl Track + Toggl 2.0"
)

func (t *Toggl) Mode() Mode {
	if t == nil {
		return ModeOff
	}
	return t.backend().mode()
}

type togglAPI interface {
	mode() Mode
	keyVar() string
	authorize(req *http.Request)
	cacheScope() string
	fetchYear(ctx context.Context, start, end time.Time) (*YearData, error)
	fetchProjects(ctx context.Context) (map[int]Project, error)
	fetchWorkspaces(ctx context.Context) ([]Workspace, error)
	fetchClients(ctx context.Context, workspaceID int) ([]Client, error)
}

func (t *Toggl) backend() togglAPI {
	if t.api != nil {
		return t.api
	}
	return trackAPI{t}
}

type fetchCall struct {
	done chan struct{}
	val  any
	err  error
}

type breakerState struct {
	failures  int
	openUntil time.Time
}

const togglBreakerThreshold = 3

var (
	togglFetchTimeout    = 2 * time.Minute
	togglBreakerCooldown = time.Minute
)

type cacheEntry struct {
	val        any
	start, end time.Time
	fetchedAt  time.Time
	stale      bool
}

func (t *Toggl) getCached(ctx context.Context, key string, start, end time.Time, fn func(context.Context) (any, error)) (any, error) {
	t.mu.Lock()
	cached, hit := t.cache[key]
	if hit && !cached.stale {
		t.mu.Unlock()
		log.Printf("toggl: %s — served from cache", key)
		return cached.val, nil
	}
	if until := t.breaker[key].openUntil; time.Now().Before(until) {
		t.mu.Unlock()
		log.Printf("toggl: %s — upstream failing, not retrying before %s", key, until.Format(time.RFC3339))
		return staleOr(key, cached, hit, fmt.Errorf("toggl: %s: upstream unavailable, not retried", key))
	}
	call, running := t.inflight[key]
	if !running {
		call = &fetchCall{done: make(chan struct{})}
		if t.inflight == nil {
			t.inflight = map[string]*fetchCall{}
		}
		t.inflight[key] = call
	}
	t.mu.Unlock()

	if !running {
		go t.fill(ctx, key, start, end, fn, call)
	} else {
		log.Printf("toggl: %s — waiting on the fetch already in flight", key)
	}

	select {
	case <-call.done:
	case <-ctx.Done():
		log.Printf("toggl: %s — gave up waiting; the fetch continues", key)
		return staleOr(key, cached, hit, ctx.Err())
	}
	if call.err != nil {
		return staleOr(key, cached, hit, call.err)
	}
	return call.val, nil
}

func (t *Toggl) fill(ctx context.Context, key string, start, end time.Time, fn func(context.Context) (any, error), call *fetchCall) {
	log.Printf("toggl: %s — fetching…", key)
	t0 := time.Now()
	fetchCtx, cancelFetch := context.WithTimeout(context.WithoutCancel(ctx), togglFetchTimeout)
	defer cancelFetch()
	val, err := fn(fetchCtx)
	elapsed := time.Since(t0).Round(time.Millisecond)

	t.mu.Lock()
	delete(t.inflight, key)
	if err != nil {
		t.recordFailureLocked(key)
		if isUnauthorized(err) {
			t.rejectedAt, t.rejection = time.Now(), err.Error()
		}
	} else {
		delete(t.breaker, key)
		t.rejectedAt, t.rejection = time.Time{}, ""
		if t.cache == nil {
			t.cache = map[string]cacheEntry{}
		}
		t.cache[key] = cacheEntry{val: val, start: start, end: end, fetchedAt: time.Now()}
	}
	call.val, call.err = val, err
	t.mu.Unlock()
	close(call.done)

	if err != nil {
		log.Printf("toggl: %s — failed after %s: %v", key, elapsed, err)
		return
	}
	log.Printf("toggl: %s — fetched in %s", key, elapsed)
}

func staleOr(key string, cached cacheEntry, hit bool, err error) (any, error) {
	if !hit {
		return nil, err
	}
	log.Printf("toggl: %s — serving stale data fetched %s", key, cached.fetchedAt.Format(time.RFC3339))
	return cached.val, nil
}

func (t *Toggl) recordFailureLocked(key string) {
	if t.breaker == nil {
		t.breaker = map[string]breakerState{}
	}
	b := t.breaker[key]
	b.failures++
	if b.failures >= togglBreakerThreshold {
		b.openUntil = time.Now().Add(togglBreakerCooldown)
		log.Printf("toggl: %s — %d consecutive failures, pausing fetches for %s", key, b.failures, togglBreakerCooldown)
	}
	t.breaker[key] = b
}

func (t *Toggl) EvictRange(start, end time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.cache {
		if e.start.IsZero() && e.end.IsZero() {
			continue
		}
		if !e.end.Before(start) && !e.start.After(end) {
			e.stale = true
			t.cache[k] = e
		}
	}
	clear(t.breaker)
}

func (t *Toggl) markYearStale(year int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := t.yearKey(year)
	if e, ok := t.cache[key]; ok {
		e.stale = true
		t.cache[key] = e
	}
}

func (t *Toggl) YearPending(year int) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := t.yearKey(year)
	_, fetching := t.inflight[key]
	_, cached := t.cache[key]
	return fetching && !cached
}

func (t *Toggl) YearStatus(year int) (fetchedAt time.Time, stale bool) {
	if t == nil {
		return time.Time{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.cache[t.yearKey(year)]; ok {
		return e.fetchedAt, e.stale
	}
	return time.Time{}, false
}

type KeyStatus struct {
	Rejected   bool
	RejectedAt time.Time
	Rejection  string
	ExpiresAt  time.Time
	Expired    bool
	Warning    string
}

const keyWarningDays = 7

func (t *Toggl) KeyStatus(today time.Time) KeyStatus {
	if t == nil {
		return KeyStatus{}
	}
	t.mu.Lock()
	s := KeyStatus{Rejected: !t.rejectedAt.IsZero(), RejectedAt: t.rejectedAt, Rejection: t.rejection, ExpiresAt: t.keyExpiresAt}
	t.mu.Unlock()
	s.Expired = s.Rejected || (!s.ExpiresAt.IsZero() && daysUntil(today, s.ExpiresAt) < 0)
	s.Warning = keyWarning(s, today, t.backend().keyVar())
	return s
}

func daysUntil(today, day time.Time) int {
	loc := today.Location()
	from := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	to := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return int(math.Round(to.Sub(from).Hours() / 24))
}

func keyWarning(s KeyStatus, today time.Time, keyVar string) string {
	if s.Rejected {
		return fmt.Sprintf("Toggl rejected the API key on %s (HTTP 401) — it has expired or been revoked. Create a new key in Toggl and set %s.",
			s.RejectedAt.In(today.Location()).Format("02 Jan 15:04"), keyVar)
	}
	if s.ExpiresAt.IsZero() {
		return ""
	}
	days := daysUntil(today, s.ExpiresAt)
	date := s.ExpiresAt.Format("02 Jan 2006")
	switch {
	case days < 0:
		return fmt.Sprintf("The Toggl API key expired on %s (%s_EXPIRES_AT) — create a new key in Toggl and set %s.", date, keyVar, keyVar)
	case days == 0:
		return fmt.Sprintf("The Toggl API key expires today, %s — create a new key in Toggl and set %s.", date, keyVar)
	case days == 1:
		return fmt.Sprintf("The Toggl API key expires tomorrow, %s — create a new key in Toggl and set %s before then.", date, keyVar)
	case days <= keyWarningDays:
		return fmt.Sprintf("The Toggl API key expires in %d days, on %s — create a new key in Toggl and set %s before then.", days, date, keyVar)
	}
	return ""
}

func (t *Toggl) yearKey(year int) string {
	return t.backend().cacheScope() + "|" + strconv.Itoa(year)
}

type Aggregate struct {
	ProjectID   int
	RateCents   int
	AmountCents int
	Seconds     int
	Currency    string
}

type Project struct {
	Name     string
	ClientID int
}

type YearData struct {
	Months map[time.Month][]Aggregate
	Days   map[string]bool
}

func (t *Toggl) Year(ctx context.Context, year int) (*YearData, error) {
	if t == nil {
		return &YearData{Months: map[time.Month][]Aggregate{}, Days: map[string]bool{}}, nil
	}
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC)
	v, err := t.getCached(ctx, t.yearKey(year), start, end, func(fetchCtx context.Context) (any, error) {
		return t.backend().fetchYear(fetchCtx, start, end)
	})
	if err != nil {
		return nil, err
	}
	return v.(*YearData), nil
}

func (t *Toggl) Projects(ctx context.Context) (map[int]Project, error) {
	if t == nil {
		return map[int]Project{}, nil
	}
	v, err := t.getCached(ctx, "projects", time.Time{}, time.Time{}, func(fetchCtx context.Context) (any, error) {
		return t.backend().fetchProjects(fetchCtx)
	})
	if err != nil {
		return nil, err
	}
	return v.(map[int]Project), nil
}

type Workspace struct {
	ID   int
	Name string
}

func (t *Toggl) Workspaces(ctx context.Context) ([]Workspace, error) {
	if t == nil {
		return nil, nil
	}
	v, err := t.getCached(ctx, "workspaces", time.Time{}, time.Time{}, func(fetchCtx context.Context) (any, error) {
		return t.backend().fetchWorkspaces(fetchCtx)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Workspace), nil
}

type Client struct {
	ID   int
	Name string
}

func (t *Toggl) Clients(ctx context.Context, workspaceID int) ([]Client, error) {
	if t == nil {
		return nil, nil
	}
	key := "clients|" + strconv.Itoa(workspaceID)
	v, err := t.getCached(ctx, key, time.Time{}, time.Time{}, func(fetchCtx context.Context) (any, error) {
		return t.backend().fetchClients(fetchCtx, workspaceID)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Client), nil
}

const (
	togglAttempts   = 3
	togglBackoffMax = 8 * time.Second
)

var togglBackoffBase = 500 * time.Millisecond

func (t *Toggl) do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("toggl: buffer request body: %w", err)
		}
	}

	backoff := togglBackoffBase
	var lastErr error
	for attempt := 1; ; attempt++ {
		resp, err := t.attempt(ctx, method, url, payload)
		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil
		}

		wait := backoff
		if err != nil {
			lastErr = err
		} else {
			lastErr = apiError("toggl "+method, resp)
			if after, ok := retryAfter(resp); ok {
				wait = after
			}
			resp.Body.Close()
		}

		if attempt >= togglAttempts {
			return nil, lastErr
		}
		log.Printf("toggl: attempt %d/%d failed (%v) — retrying in %s", attempt, togglAttempts, lastErr, wait)
		if err := sleepCtx(ctx, wait); err != nil {
			return nil, lastErr
		}
		if backoff *= 2; backoff > togglBackoffMax {
			backoff = togglBackoffMax
		}
	}
}

func (t *Toggl) attempt(ctx context.Context, method, url string, payload []byte) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	t.backend().authorize(req)
	return t.client().Do(req)
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *Toggl) client() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return http.DefaultClient
}

type statusError struct {
	API    string
	Status int
	Body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", e.API, e.Status, e.Body)
}

func apiError(api string, resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &statusError{API: api, Status: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
}

func isUnauthorized(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.Status == http.StatusUnauthorized
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func parseIDs(s string) []int {
	var ids []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n, err := strconv.Atoi(part); err == nil {
			ids = append(ids, n)
		}
	}
	return ids
}
