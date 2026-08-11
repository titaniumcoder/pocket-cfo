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

// Toggl talks to the Toggl Track API: the Reports API v3 detailed report for
// tracked time + Toggl-calculated billable amounts, and the v9 API for project
// names. Tracked time uses Toggl's individual rounding to 15 minutes and only
// counts billable entries.
//
// Responses are cached in memory indefinitely to stay under Toggl's rate limit.
// An entry is only ever invalidated explicitly, via EvictRange — the page's
// Reload button, which marks the currently viewed month or year stale. Stale
// means "refetch on next use", not "forget": the last good value is kept as a
// fallback, because Toggl's detailed report is slow enough to time out
// routinely and an out-of-date figure with an honest timestamp is worth far
// more to the reader than an empty Income panel. See getCached.
type Toggl struct {
	Token       string
	WorkspaceID string
	ProjectIDs  string // optional comma-separated filter
	HTTP        *http.Client

	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]*fetchCall
	breaker  map[string]breakerState
}

// fetchCall is one in-progress fetch that later callers for the same key wait
// on instead of starting their own. val/err are written once, before done is
// closed, and only read after — the close is the happens-before edge.
type fetchCall struct {
	done chan struct{}
	val  any
	err  error
}

// breakerState tracks consecutive failures per cache key. Once openUntil is in
// the future no fetch is attempted at all: a Toggl outage otherwise turns every
// page load into a fresh multi-second timeout, and those pile up.
type breakerState struct {
	failures  int
	openUntil time.Time
}

const togglBreakerThreshold = 3

// togglBreakerCooldown is how long a key is left alone after the threshold is
// reached. A variable only so tests need not wait it out.
var togglBreakerCooldown = time.Minute

type cacheEntry struct {
	val        any
	start, end time.Time // date range this entry covers (zero = not range-scoped)
	fetchedAt  time.Time // when val was last fetched successfully
	// stale marks an entry whose value is no longer trusted to be current:
	// EvictRange set it (the Reload link), or a refresh failed. Either way
	// val is retained — see getCached, which refetches a stale entry and
	// falls back to val when that refetch fails.
	stale bool
}

// getCached returns the cached value for key, or computes it via fn and stores it
// forever, tagged with the date range it covers (zero start/end for entries that
// aren't range-scoped, such as the project list). Cache hits, fetch starts, and
// fetch outcomes (with timing) are logged to stdout for container log visibility.
//
// A fetch failure is only an error when there is nothing cached to fall back
// on. With a previous value in hand this returns that value and no error,
// leaving the entry stale so the next caller tries again; fetchedAt keeps
// pointing at when the data was genuinely current, which is what the dashboard
// shows the reader (see Tracker.compute's TogglStaleNote). The alternative —
// propagating the error — blanks the whole Income panel over one slow request
// to an API that times out routinely.
func (t *Toggl) getCached(ctx context.Context, key string, start, end time.Time, fn func() (any, error)) (any, error) {
	t.mu.Lock()
	cached, hit := t.cache[key]
	if hit && !cached.stale {
		t.mu.Unlock()
		log.Printf("toggl: %s — served from cache", key)
		return cached.val, nil
	}
	// Upstream has been failing: don't add another slow request to the pile.
	if until := t.breaker[key].openUntil; time.Now().Before(until) {
		t.mu.Unlock()
		log.Printf("toggl: %s — upstream failing, not retrying before %s", key, until.Format(time.RFC3339))
		return staleOr(key, cached, hit, fmt.Errorf("toggl: %s: upstream unavailable, not retried", key))
	}
	// One fetch per key: N concurrent readers of the same year would
	// otherwise fire N identical year-wide reports at an API that is already
	// the slow part.
	if call, ok := t.inflight[key]; ok {
		t.mu.Unlock()
		log.Printf("toggl: %s — waiting on the fetch already in flight", key)
		select {
		case <-call.done:
		case <-ctx.Done():
			// The leader can be the background refresher, which has a much
			// longer deadline than a page request and keeps running after
			// this returns. Waiting it out would hand the caller's whole
			// budget to someone else's fetch, so give up on our own terms
			// and let the refresher finish in its own time.
			log.Printf("toggl: %s — gave up waiting on the in-flight fetch", key)
			return staleOr(key, cached, hit, ctx.Err())
		}
		if call.err != nil {
			return staleOr(key, cached, hit, call.err)
		}
		return call.val, nil
	}
	call := &fetchCall{done: make(chan struct{})}
	if t.inflight == nil {
		t.inflight = map[string]*fetchCall{}
	}
	t.inflight[key] = call
	t.mu.Unlock()

	log.Printf("toggl: %s — fetching…", key)
	t0 := time.Now()
	val, err := fn()
	elapsed := time.Since(t0).Round(time.Millisecond)

	// The result is published to waiters and the shared maps under one
	// acquisition, so no caller can observe the key as neither in-flight nor
	// resolved. close(done) happens after the unlock; waiters read val/err
	// only after receiving on done, which the close orders for them.
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
		return staleOr(key, cached, hit, err)
	}
	log.Printf("toggl: %s — fetched in %s", key, elapsed)
	return val, nil
}

// staleOr returns the previous value for key when there is one, and err
// otherwise — the single place that decides a failed fetch degrades to old
// data rather than to nothing.
func staleOr(key string, cached cacheEntry, hit bool, err error) (any, error) {
	if !hit {
		return nil, err
	}
	log.Printf("toggl: %s — serving stale data fetched %s", key, cached.fetchedAt.Format(time.RFC3339))
	return cached.val, nil
}

// recordFailureLocked counts a failed fetch and opens the breaker once the
// threshold is reached. Failures are deliberately not reset when it opens: the
// first attempt after the cooldown is a probe, and if that fails too the
// breaker reopens immediately rather than granting another three tries.
// t.mu must be held.
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

// EvictRange marks cached responses whose date range intersects [start, end]
// stale, so the next read refetches them. Entries that aren't range-scoped
// (e.g. the project list) are left alone. A nil Toggl (the tracked-hours layer
// disabled by config — see Tracker) is a no-op, same convention as the other
// methods below.
//
// Deliberately a mark rather than a delete: deleting would make a failed
// refetch indistinguishable from "never fetched", which is precisely the case
// where the previous figures are still the best answer available. Entries are
// therefore never removed, which is fine — the key space is one entry per year
// plus one project list.
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
	// Reload is an explicit "try again now", so it also reopens every circuit
	// breaker — otherwise the button would quietly do nothing for up to a
	// minute after an outage. Cleared wholesale rather than per matching cache
	// entry, because the case that matters most is a key whose very first
	// fetch failed: it has no cache entry for the loop above to match on.
	clear(t.breaker)
}

// markStale forces the next read of key to refetch, leaving any circuit
// breaker alone. This is the background refresher's invalidation, as distinct
// from EvictRange's: Reload is the reader saying "try again now, ignore the
// breaker", whereas a scheduled refresh has no business overriding a decision
// to stop calling a failing API.
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

// YearStatus reports when the detailed report for the given year was last
// fetched successfully, and whether that value is currently stale — awaiting a
// refetch, or standing in for one that failed. An uncached year, or a nil Toggl,
// reports the zero time and false.
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

// Aggregate is tracked work summed per project + rate.
type Aggregate struct {
	ProjectID   int
	RateCents   int // hourly rate in cents
	AmountCents int // Toggl-calculated billable amount in cents
	Seconds     int // rounded tracked seconds
	Currency    string
}

// Project is a Toggl project. ClientID is 0 when the project has no client
// assigned in Toggl.
type Project struct {
	Name     string
	ClientID int
}

const (
	roundingNearest = 0 // -1 down, 0 nearest, 1 up
	roundingMinutes = 15
)

// detailedRow is one row of the Toggl detailed search response.
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

// eachDetailedRow pages through the billable detailed report for [start, end]
// (inclusive) and calls fn for every row.
//
// Only first_row_number is sent back on follow-up pages — deliberately not
// first_id, even though Toggl also returns an X-Next-ID header alongside
// X-Next-Row-Number (seemingly meant as a compound cursor). Sending both
// together makes Toggl's API silently return zero rows (200 OK, empty body)
// for the follow-up request despite the first page's header claiming more
// data exists — confirmed by hand against the real API: a year-wide query
// paginating past 50 rows came back empty on page 2 with first_id included,
// and returned the correct remaining rows with first_id dropped. This is why
// single-page queries (a single month, or any range under ~50 rows) always
// looked correct while a year-wide query silently lost every row past the
// first page — e.g. July showing 34 tracked hours instead of the real 63.
func (t *Toggl) eachDetailedRow(ctx context.Context, start, end time.Time, fn func(detailedRow)) error {
	firstRow := 0
	for page := 0; page < 100; page++ { // safety cap
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

// YearData is a whole calendar year's billable work: tracked work aggregated per
// project + rate within each month, plus the set of calendar days that have any
// billable time. Fetched and cached as a single yearly detailed report so the
// month and year views share one Toggl call.
type YearData struct {
	Months map[time.Month][]Aggregate // per month, aggregated per project + rate
	Days   map[string]bool            // YYYY-MM-DD that have billable time
}

// Year returns the given calendar year's billable work, fetched as one detailed
// report (with Toggl's individual 15-minute rounding) and cached. Callers must
// treat the result as read-only — the same pointer is shared across calls. A
// nil Toggl (the tracked-hours layer disabled by config — see Tracker) always
// returns an empty, error-free YearData, so callers need no separate
// "tracking disabled" branch of their own.
func (t *Toggl) Year(ctx context.Context, year int) (*YearData, error) {
	if t == nil {
		return &YearData{Months: map[time.Month][]Aggregate{}, Days: map[string]bool{}}, nil
	}
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC)
	v, err := t.getCached(ctx, t.yearKey(year), start, end, func() (any, error) {
		return t.fetchYear(ctx, start, end)
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
		earliest := "" // earliest entry date in the row (YYYY-MM-DD)
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
		// Bucket the whole row into the month of its earliest entry. The billable
		// amount is per row, so a row is attributed to a single month; in practice
		// a row is one time entry on one day, so this is exact.
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

// Projects returns all workspace projects (active and archived) keyed by id.
// Not range-scoped, so it survives a Reload and is only refetched on restart.
// A nil Toggl always returns an empty, error-free map.
func (t *Toggl) Projects(ctx context.Context) (map[int]Project, error) {
	if t == nil {
		return map[int]Project{}, nil
	}
	v, err := t.getCached(ctx, "projects", time.Time{}, time.Time{}, func() (any, error) {
		return t.fetchProjects(ctx)
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

// Workspace is a Toggl workspace (used by the admin /info diagnostics page to
// help pick the right IDs for config.json/recipient tracking_client_id).
type Workspace struct {
	ID   int
	Name string
}

// Workspaces returns every workspace the configured token can see. Not
// range-scoped, so it's cached forever like Projects.
func (t *Toggl) Workspaces(ctx context.Context) ([]Workspace, error) {
	if t == nil {
		return nil, nil
	}
	v, err := t.getCached(ctx, "workspaces", time.Time{}, time.Time{}, func() (any, error) {
		return t.fetchWorkspaces(ctx)
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

// Client is a Toggl client (the entity tracking_client_id links a recipient
// to) — used by the admin /info diagnostics page.
type Client struct {
	ID   int
	Name string
}

// Clients returns every client in the given workspace. Not range-scoped, so
// it's cached forever like Projects/Workspaces.
func (t *Toggl) Clients(ctx context.Context, workspaceID int) ([]Client, error) {
	if t == nil {
		return nil, nil
	}
	key := "clients|" + strconv.Itoa(workspaceID)
	v, err := t.getCached(ctx, key, time.Time{}, time.Time{}, func() (any, error) {
		return t.fetchClients(ctx, workspaceID)
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
	// togglAttempts is how many times one request is tried in total, not how
	// many times it is retried.
	togglAttempts   = 3
	togglBackoffMax = 8 * time.Second
)

// togglBackoffBase is the first retry delay, doubling from there. A variable
// rather than a constant purely so the package's own tests can shrink it —
// otherwise every test that exercises a failing endpoint pays two real sleeps
// (see TestMain in fake_test.go). Nothing outside tests writes it.
var togglBackoffBase = 500 * time.Millisecond

// do performs one Toggl API call, retrying a transient failure up to
// togglAttempts times with exponential backoff.
//
// Worth retrying: a transport error (the detailed report times out often
// enough that a second attempt frequently succeeds) and any 429 or 5xx —
// Toggl rate-limits the reporting endpoints and, until now, the app simply
// surfaced the 429 as a failure. A 4xx other than 429 is returned to the
// caller untouched: retrying a 401 or a 404 only wastes the request budget.
//
// Retries are bounded by ctx, not just by the attempt count, so nothing here
// can outlive the caller's deadline (see requestTimeout in cmd/pocketcfo) —
// on a tight deadline this degrades to a single attempt, which is the same
// behaviour as before.
func (t *Toggl) do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	// Buffered so each attempt gets a fresh reader — a retry can't re-read
	// a stream the previous attempt already consumed.
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

// retryableStatus reports whether a status code is worth another attempt.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// retryAfter reads a Retry-After expressed in seconds, which is the form
// Toggl sends. An HTTP-date, a malformed value or no header at all yields
// ok=false, and the caller falls back to its own backoff.
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

// sleepCtx waits for d, or returns early with ctx's error if the caller gives
// up first — so a long Retry-After can never hold a request past its deadline.
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
