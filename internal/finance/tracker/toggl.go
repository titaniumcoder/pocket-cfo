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

	mu    sync.Mutex
	cache map[string]cacheEntry
}

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
func (t *Toggl) getCached(key string, start, end time.Time, fn func() (any, error)) (any, error) {
	t.mu.Lock()
	cached, hit := t.cache[key]
	t.mu.Unlock()
	if hit && !cached.stale {
		log.Printf("toggl: %s — served from cache", key)
		return cached.val, nil
	}

	log.Printf("toggl: %s — fetching…", key)
	t0 := time.Now()
	val, err := fn()
	elapsed := time.Since(t0).Round(time.Millisecond)
	if err != nil {
		log.Printf("toggl: %s — failed after %s: %v", key, elapsed, err)
		if !hit {
			return nil, err
		}
		log.Printf("toggl: %s — serving stale data fetched %s", key, cached.fetchedAt.Format(time.RFC3339))
		return cached.val, nil
	}
	log.Printf("toggl: %s — fetched in %s", key, elapsed)

	t.mu.Lock()
	if t.cache == nil {
		t.cache = map[string]cacheEntry{}
	}
	t.cache[key] = cacheEntry{val: val, start: start, end: end, fetchedAt: time.Now()}
	t.mu.Unlock()
	return val, nil
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
	v, err := t.getCached(t.yearKey(year), start, end, func() (any, error) {
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
	v, err := t.getCached("projects", time.Time{}, time.Time{}, func() (any, error) {
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
	v, err := t.getCached("workspaces", time.Time{}, time.Time{}, func() (any, error) {
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
	v, err := t.getCached(key, time.Time{}, time.Time{}, func() (any, error) {
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

func (t *Toggl) do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	auth := base64.StdEncoding.EncodeToString([]byte(t.Token + ":api_token"))
	req.Header.Set("Authorization", "Basic "+auth)
	return t.client().Do(req)
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
