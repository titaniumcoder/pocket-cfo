package tracker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]*fetchCall
	breaker  map[string]breakerState
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
	} else {
		delete(t.breaker, key)
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

func (t *Toggl) markStale(key string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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

func (t *Toggl) yearKey(year int) string {
	return "detailed|" + t.ProjectIDs + "|" + strconv.Itoa(year)
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

const (
	roundingNearest = 0
	roundingMinutes = 15
)

type detailedRow struct {
	ProjectID             *int   `json:"project_id"`
	HourlyRateInCents     *int   `json:"hourly_rate_in_cents"`
	BillableAmountInCents *int   `json:"billable_amount_in_cents"`
	Currency              string `json:"currency"`
	TimeEntries           []struct {
		Seconds int    `json:"seconds"`
		Start   string `json:"start"`
	} `json:"time_entries"`
}

func (t *Toggl) eachDetailedRow(ctx context.Context, start, end time.Time, fn func(detailedRow)) error {
	firstRow := 0
	for page := 0; page < 100; page++ {
		body := map[string]any{
			"start_date":       start.Format("2006-01-02"),
			"end_date":         end.Format("2006-01-02"),
			"billable":         true,
			"rounding":         roundingNearest,
			"rounding_minutes": roundingMinutes,
		}
		if ids := parseIDs(t.ProjectIDs); len(ids) > 0 {
			body["project_ids"] = ids
		}
		if firstRow > 0 {
			body["first_row_number"] = firstRow
		}

		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}

		url := fmt.Sprintf("https://api.track.toggl.com/reports/api/v3/workspace/%s/search/time_entries", t.WorkspaceID)
		resp, err := t.do(ctx, http.MethodPost, url, bytes.NewReader(buf))
		if err != nil {
			return err
		}

		if resp.StatusCode != http.StatusOK {
			err := apiError("toggl", resp)
			resp.Body.Close()
			return err
		}

		var rows []detailedRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			resp.Body.Close()
			return fmt.Errorf("toggl: decode detailed: %w", err)
		}
		nextRow := resp.Header.Get("X-Next-Row-Number")
		resp.Body.Close()

		for _, r := range rows {
			fn(r)
		}

		if nextRow == "" {
			break
		}
		firstRow, _ = strconv.Atoi(nextRow)
	}
	return nil
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
		return t.fetchYear(fetchCtx, start, end)
	})
	if err != nil {
		return nil, err
	}
	return v.(*YearData), nil
}

func (t *Toggl) fetchYear(ctx context.Context, start, end time.Time) (*YearData, error) {
	type key struct {
		pid, rate int
		month     time.Month
	}
	acc := map[key]*Aggregate{}
	days := map[string]bool{}

	err := t.eachDetailedRow(ctx, start, end, func(r detailedRow) {
		pid := derefInt(r.ProjectID)
		rate := derefInt(r.HourlyRateInCents)
		amount := derefInt(r.BillableAmountInCents)
		sec := 0
		earliest := ""
		for _, te := range r.TimeEntries {
			sec += te.Seconds
			if te.Seconds <= 0 || len(te.Start) < 10 {
				continue
			}
			d := te.Start[:10]
			days[d] = true
			if earliest == "" || d < earliest {
				earliest = d
			}
		}
		month := start.Month()
		if tm, perr := time.Parse("2006-01-02", earliest); perr == nil {
			month = tm.Month()
		}
		k := key{pid, rate, month}
		a := acc[k]
		if a == nil {
			a = &Aggregate{ProjectID: pid, RateCents: rate, Currency: r.Currency}
			acc[k] = a
		}
		a.AmountCents += amount
		a.Seconds += sec
	})
	if err != nil {
		return nil, err
	}

	yd := &YearData{Months: map[time.Month][]Aggregate{}, Days: days}
	for k, a := range acc {
		yd.Months[k.month] = append(yd.Months[k.month], *a)
	}
	return yd, nil
}

func (t *Toggl) Projects(ctx context.Context) (map[int]Project, error) {
	if t == nil {
		return map[int]Project{}, nil
	}
	v, err := t.getCached(ctx, "projects", time.Time{}, time.Time{}, func(fetchCtx context.Context) (any, error) {
		return t.fetchProjects(fetchCtx)
	})
	if err != nil {
		return nil, err
	}
	return v.(map[int]Project), nil
}

func (t *Toggl) fetchProjects(ctx context.Context) (map[int]Project, error) {
	url := fmt.Sprintf("https://api.track.toggl.com/api/v9/workspaces/%s/projects?active=both&per_page=500", t.WorkspaceID)
	resp, err := t.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, apiError("toggl", resp)
	}

	var list []struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		ClientID *int   `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("toggl: decode projects: %w", err)
	}

	out := make(map[int]Project, len(list))
	for _, p := range list {
		out[p.ID] = Project{Name: p.Name, ClientID: derefInt(p.ClientID)}
	}
	return out, nil
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
		return t.fetchWorkspaces(fetchCtx)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Workspace), nil
}

func (t *Toggl) fetchWorkspaces(ctx context.Context) ([]Workspace, error) {
	resp, err := t.do(ctx, http.MethodGet, "https://api.track.toggl.com/api/v9/me/workspaces", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, apiError("toggl", resp)
	}

	var list []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("toggl: decode workspaces: %w", err)
	}

	out := make([]Workspace, len(list))
	for i, w := range list {
		out[i] = Workspace{ID: w.ID, Name: w.Name}
	}
	return out, nil
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
		return t.fetchClients(fetchCtx, workspaceID)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Client), nil
}

func (t *Toggl) fetchClients(ctx context.Context, workspaceID int) ([]Client, error) {
	url := fmt.Sprintf("https://api.track.toggl.com/api/v9/workspaces/%d/clients", workspaceID)
	resp, err := t.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, apiError("toggl", resp)
	}

	var list []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("toggl: decode clients: %w", err)
	}

	out := make([]Client, len(list))
	for i, c := range list {
		out[i] = Client{ID: c.ID, Name: c.Name}
	}
	return out, nil
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
	auth := base64.StdEncoding.EncodeToString([]byte(t.Token + ":api_token"))
	req.Header.Set("Authorization", "Basic "+auth)
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

func apiError(api string, resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s: status %d: %s", api, resp.StatusCode, strings.TrimSpace(string(msg)))
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
