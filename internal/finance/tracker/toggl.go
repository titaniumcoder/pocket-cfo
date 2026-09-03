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
	"slices"
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
	Loc         *time.Location
	CacheDir    string

	api          togglAPI
	keyExpiresAt time.Time

	mu         sync.Mutex
	persistMu  sync.Mutex
	restored   bool
	generation int
	cache      map[string]cacheEntry
	inflight   map[string]*fetchCall
	breaker    map[string]breakerState
	rejectedAt time.Time
	rejection  string

	quotaKnown     bool
	quotaRemaining int
	quotaResetAt   time.Time
	quotaGateUntil time.Time
	headersSeen    map[string]bool
	quotaHeaders   map[string]string

	counters cacheCounters
}

type cacheCounters struct {
	Hits, StaleServed, Fetches, Failures, Requests, Retries int
	LastFetchAt                                             time.Time
	LastFetchTook                                           time.Duration
}

type HoursSource interface {
	Year(ctx context.Context, year int) (*YearData, error)
	Projects(ctx context.Context) (map[int]Project, error)
	Pending(start, end time.Time) bool
	Status(start, end time.Time) (fetchedAt time.Time, stale bool)
	EvictRange(start, end time.Time)
	markStale(start, end time.Time, olderThan time.Duration)
	KeyStatus(today time.Time) KeyStatus
	Quota(now time.Time) QuotaStatus
	Reset()
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
	fetchRange(ctx context.Context, start, end time.Time) (*YearData, error)
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
	done       chan struct{}
	start, end time.Time
	generation int
	val        any
	err        error
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

type entryKind string

const (
	kindMonth    entryKind = "month"
	kindProjects entryKind = "projects"
	kindRates    entryKind = "rates"
	kindOther    entryKind = ""
)

type cacheEntry struct {
	val        any
	kind       entryKind
	start, end time.Time
	fetchedAt  time.Time
	stale      bool
}

func (t *Toggl) getCached(ctx context.Context, key string, start, end time.Time, fn func(context.Context) (any, error)) (any, error) {
	return t.getCachedAs(ctx, key, kindOther, start, end, fn)
}

func (t *Toggl) getCachedAs(ctx context.Context, key string, kind entryKind, start, end time.Time, fn func(context.Context) (any, error)) (any, error) {
	t.lock()
	cached, hit := t.cache[key]
	if hit && !cached.stale {
		t.counters.Hits++
		t.mu.Unlock()
		log.Printf("toggl: %s — served from cache", key)
		return cached.val, nil
	}
	if until, gated := t.gatedLocked(); gated {
		t.mu.Unlock()
		log.Printf("toggl: %s — hourly quota used up, not asking before %s", key, until.Format(time.RFC3339))
		return t.staleOr(key, cached, hit, &quotaError{resetAt: until})
	}
	if until, blocked := t.blockedLocked(key); blocked {
		t.mu.Unlock()
		log.Printf("toggl: %s — upstream failing, not retrying before %s", key, until.Format(time.RFC3339))
		return t.staleOr(key, cached, hit, fmt.Errorf("toggl: %s: upstream unavailable, not retried", key))
	}
	call, leader := t.joinLocked(key, start, end)
	t.mu.Unlock()

	if leader {
		go t.fill(ctx, key, fn, call, func(val any, at time.Time) {
			t.cache[key] = cacheEntry{val: val, kind: kind, start: start, end: end, fetchedAt: at}
		})
	} else {
		log.Printf("toggl: %s — waiting on the fetch already in flight", key)
	}

	select {
	case <-call.done:
	case <-ctx.Done():
		log.Printf("toggl: %s — gave up waiting; the fetch continues", key)
		return t.staleOr(key, cached, hit, ctx.Err())
	}
	if call.err != nil {
		return t.staleOr(key, cached, hit, call.err)
	}
	return call.val, nil
}

func (t *Toggl) blockedLocked(key string) (time.Time, bool) {
	until := t.breaker[key].openUntil
	return until, time.Now().Before(until)
}

func (t *Toggl) gatedLocked() (time.Time, bool) {
	return t.quotaGateUntil, time.Now().Before(t.quotaGateUntil)
}

func (t *Toggl) joinLocked(key string, start, end time.Time) (call *fetchCall, leader bool) {
	if call, running := t.inflight[key]; running {
		return call, false
	}
	call = &fetchCall{done: make(chan struct{}), start: start, end: end, generation: t.generation}
	if t.inflight == nil {
		t.inflight = map[string]*fetchCall{}
	}
	t.inflight[key] = call
	return call, true
}

func (t *Toggl) fill(ctx context.Context, key string, fn func(context.Context) (any, error), call *fetchCall, store func(val any, at time.Time)) {
	log.Printf("toggl: %s — fetching…", key)
	t0 := time.Now()
	fetchCtx, cancelFetch := context.WithTimeout(context.WithoutCancel(ctx), togglFetchTimeout)
	defer cancelFetch()
	val, err := fn(fetchCtx)
	elapsed := time.Since(t0).Round(time.Millisecond)

	t.lock()
	if t.inflight[key] == call {
		delete(t.inflight, key)
	}
	t.counters.Fetches++
	t.counters.LastFetchAt, t.counters.LastFetchTook = time.Now(), elapsed
	if err != nil {
		t.counters.Failures++
	}
	stored := false
	switch {
	case err != nil:
		if !isQuotaExhausted(err) {
			t.recordFailureLocked(key)
		}
		if isUnauthorized(err) {
			t.rejectedAt, t.rejection = time.Now(), err.Error()
		}
	case call.generation != t.generation:
		log.Printf("toggl: %s — discarded, the cache was reset while it was fetching", key)
	default:
		delete(t.breaker, key)
		t.rejectedAt, t.rejection = time.Time{}, ""
		if t.cache == nil {
			t.cache = map[string]cacheEntry{}
		}
		store(val, time.Now())
		stored = true
	}
	call.val, call.err = val, err
	t.mu.Unlock()
	if stored {
		t.persist()
	}
	close(call.done)

	if err != nil {
		log.Printf("toggl: %s — failed after %s: %v", key, elapsed, err)
		return
	}
	log.Printf("toggl: %s — fetched in %s", key, elapsed)
}

func (t *Toggl) staleOr(key string, cached cacheEntry, hit bool, err error) (any, error) {
	if !hit {
		return nil, err
	}
	t.mu.Lock()
	t.counters.StaleServed++
	t.mu.Unlock()
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
	t.lock()
	for k, e := range t.cache {
		if e.start.IsZero() && e.end.IsZero() {
			continue
		}
		if overlaps(e.start, e.end, start, end) {
			e.stale = true
			t.cache[k] = e
		}
	}
	clear(t.breaker)
	t.mu.Unlock()
	t.persist()
}

func (t *Toggl) Reset() {
	if t == nil {
		return
	}
	t.lock()
	defer t.mu.Unlock()
	t.generation++
	clear(t.cache)
	clear(t.breaker)
	clear(t.inflight)
	t.rejectedAt, t.rejection = time.Time{}, ""
	t.removeSnapshotLocked()
	log.Printf("toggl: %s cache reset — every month, project and rate is fetched afresh", t.backend().cacheScope())
}

func (t *Toggl) markStale(start, end time.Time, olderThan time.Duration) {
	if t == nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	t.lock()
	defer t.mu.Unlock()
	for k, e := range t.cache {
		if e.kind == kindMonth && overlaps(e.start, e.end, start, end) && e.fetchedAt.Before(cutoff) {
			e.stale = true
			t.cache[k] = e
		}
	}
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return !aEnd.Before(bStart) && !aStart.After(bEnd)
}

func (t *Toggl) Pending(start, end time.Time) bool {
	if t == nil {
		return false
	}
	t.lock()
	defer t.mu.Unlock()
	for _, call := range t.inflight {
		if !overlaps(call.start, call.end, start, end) {
			continue
		}
		for _, m := range monthsIn(call.start, call.end, t.location()) {
			if _, cached := t.cache[t.monthKey(m)]; !cached && overlaps(m.start, m.end, start, end) {
				return true
			}
		}
	}
	return false
}

func (t *Toggl) Status(start, end time.Time) (fetchedAt time.Time, stale bool) {
	if t == nil {
		return time.Time{}, false
	}
	t.lock()
	defer t.mu.Unlock()
	for _, e := range t.cache {
		if e.kind != kindMonth || !overlaps(e.start, e.end, start, end) {
			continue
		}
		if fetchedAt.IsZero() || e.fetchedAt.Before(fetchedAt) {
			fetchedAt = e.fetchedAt
		}
		stale = stale || e.stale
	}
	return fetchedAt, stale
}

type QuotaStatus struct {
	Remaining int
	ResetAt   time.Time
	Exhausted bool
	Note      string
}

const (
	quotaReserve       = 5
	quotaGateSlack     = 10 * time.Second
	defaultQuotaWindow = time.Hour
)

func (t *Toggl) Quota(now time.Time) QuotaStatus {
	if t == nil {
		return QuotaStatus{Remaining: -1}
	}
	t.lock()
	defer t.mu.Unlock()
	s := QuotaStatus{Remaining: -1, ResetAt: t.quotaResetAt}
	if t.quotaKnown && now.Before(t.quotaResetAt) {
		s.Remaining = t.quotaRemaining
	}
	if now.Before(t.quotaGateUntil) {
		s.Exhausted = true
		s.ResetAt = t.quotaGateUntil
		s.Remaining = 0
		s.Note = fmt.Sprintf("Toggl's hourly request quota is used up — tracked hours refresh again at %s.", t.quotaGateUntil.In(now.Location()).Format("15:04"))
	}
	return s
}

func (s QuotaStatus) BelowReserve() bool {
	return s.Exhausted || (s.Remaining >= 0 && s.Remaining < quotaReserve)
}

func (t *Toggl) noteQuota(resp *http.Response) {
	remaining, hasRemaining := headerInt(resp, "X-Toggl-Quota-Remaining")
	resetsIn, hasReset := headerInt(resp, "X-Toggl-Quota-Resets-In")
	exhausted := resp.StatusCode == http.StatusPaymentRequired
	now := time.Now()
	t.lock()
	defer t.mu.Unlock()
	t.noteHeadersLocked(resp.Header)
	if !hasRemaining && !hasReset && !exhausted {
		return
	}
	if hasRemaining {
		t.quotaKnown, t.quotaRemaining = true, remaining
	}
	window := defaultQuotaWindow
	if hasReset {
		window = time.Duration(resetsIn) * time.Second
	}
	if hasReset || exhausted || t.quotaResetAt.Before(now) {
		t.quotaResetAt = now.Add(window)
	}
	if exhausted {
		t.quotaKnown, t.quotaRemaining = true, 0
		t.quotaGateUntil = now.Add(window + quotaGateSlack)
		log.Printf("toggl: hourly quota used up (HTTP 402) — no requests before %s", t.quotaGateUntil.Format(time.RFC3339))
	}
}

func (t *Toggl) noteHeadersLocked(h http.Header) {
	first := t.headersSeen == nil
	if first {
		t.headersSeen, t.quotaHeaders = map[string]bool{}, map[string]string{}
	}
	var names []string
	for name, values := range h {
		t.headersSeen[name] = true
		names = append(names, name)
		if looksLikeQuota(name) && len(values) > 0 {
			t.quotaHeaders[name] = values[0]
		}
	}
	if first {
		slices.Sort(names)
		log.Printf("toggl: %s — first answer carried the headers %s", t.backend().mode(), strings.Join(names, ", "))
	}
}

func looksLikeQuota(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "quota") || strings.Contains(lower, "limit") || strings.Contains(lower, "reset") || strings.Contains(lower, "retry")
}

func headerInt(resp *http.Response, name string) (int, bool) {
	v := strings.TrimSpace(resp.Header.Get(name))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

type quotaError struct {
	resetAt time.Time
}

func (e *quotaError) Error() string {
	return fmt.Sprintf("toggl: hourly request quota used up, next attempt at %s", e.resetAt.Format(time.RFC3339))
}

func isQuotaExhausted(err error) bool {
	var qe *quotaError
	if errors.As(err, &qe) {
		return true
	}
	var se *statusError
	return errors.As(err, &se) && se.Status == http.StatusPaymentRequired
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
	t.lock()
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
		return emptyYearData(), nil
	}
	var fetchErr error
	for _, run := range t.staleRuns(year) {
		if err := t.fetchRun(ctx, run); err != nil && fetchErr == nil {
			fetchErr = err
		}
	}
	return t.assembleYear(year, fetchErr)
}

func emptyYearData() *YearData {
	return &YearData{Months: map[time.Month][]Aggregate{}, Days: map[string]bool{}}
}

type monthRange struct {
	year       int
	month      time.Month
	start, end time.Time
}

func monthOf(year int, month time.Month, loc *time.Location) monthRange {
	start := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	return monthRange{year: year, month: month, start: start, end: start.AddDate(0, 1, -1)}
}

func monthsIn(start, end time.Time, loc *time.Location) []monthRange {
	var out []monthRange
	for m := monthOf(start.Year(), start.Month(), loc); !m.start.After(end); m = monthOf(m.start.Year(), m.start.Month()+1, loc) {
		out = append(out, m)
	}
	return out
}

func (t *Toggl) location() *time.Location {
	if t.Loc == nil {
		return time.UTC
	}
	return t.Loc
}

func (t *Toggl) monthKey(m monthRange) string {
	return t.backend().cacheScope() + "|" + m.start.Format("2006-01")
}

type monthRun struct {
	first, last monthRange
}

func (r monthRun) months(loc *time.Location) []monthRange {
	return monthsIn(r.first.start, r.last.end, loc)
}

func (t *Toggl) runKey(r monthRun) string {
	return t.backend().cacheScope() + "|" + r.first.start.Format("2006-01") + ".." + r.last.start.Format("2006-01")
}

func (t *Toggl) staleRuns(year int) []monthRun {
	t.lock()
	defer t.mu.Unlock()
	var runs []monthRun
	for month := time.December; month >= time.January; month-- {
		m := monthOf(year, month, t.location())
		if e, ok := t.cache[t.monthKey(m)]; ok && !e.stale {
			continue
		}
		if n := len(runs); n > 0 && runs[n-1].first.month == month+1 {
			runs[n-1].first = m
			continue
		}
		runs = append(runs, monthRun{first: m, last: m})
	}
	return runs
}

func (t *Toggl) fetchRun(ctx context.Context, run monthRun) error {
	key := t.runKey(run)
	t.lock()
	if until, gated := t.gatedLocked(); gated {
		t.mu.Unlock()
		log.Printf("toggl: %s — hourly quota used up, not asking before %s", key, until.Format(time.RFC3339))
		return &quotaError{resetAt: until}
	}
	if until, blocked := t.blockedLocked(key); blocked {
		t.mu.Unlock()
		log.Printf("toggl: %s — upstream failing, not retrying before %s", key, until.Format(time.RFC3339))
		return fmt.Errorf("toggl: %s: upstream unavailable, not retried", key)
	}
	call, leader := t.joinLocked(key, run.first.start, run.last.end)
	t.mu.Unlock()

	if leader {
		fetch := func(fetchCtx context.Context) (any, error) {
			return t.backend().fetchRange(fetchCtx, run.first.start, run.last.end)
		}
		go t.fill(ctx, key, fetch, call, func(val any, at time.Time) {
			for m, part := range splitMonths(val.(*YearData), run.months(t.location())) {
				t.cache[t.monthKey(m)] = cacheEntry{val: part, kind: kindMonth, start: m.start, end: m.end, fetchedAt: at}
			}
		})
	} else {
		log.Printf("toggl: %s — waiting on the fetch already in flight", key)
	}

	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		log.Printf("toggl: %s — gave up waiting; the fetch continues", key)
		return ctx.Err()
	}
}

func splitMonths(yd *YearData, months []monthRange) map[monthRange]*YearData {
	parts := make(map[monthRange]*YearData, len(months))
	for _, m := range months {
		parts[m] = emptyYearData()
	}
	clamp := func(month time.Month) monthRange {
		switch {
		case month < months[0].month:
			return months[0]
		case month > months[len(months)-1].month:
			return months[len(months)-1]
		}
		return months[month-months[0].month]
	}
	for month, aggs := range yd.Months {
		parts[clamp(month)].Months[month] = aggs
	}
	for day := range yd.Days {
		month := months[0].month
		if d, err := time.Parse("2006-01-02", day); err == nil {
			month = d.Month()
		}
		parts[clamp(month)].Days[day] = true
	}
	return parts
}

func (t *Toggl) assembleYear(year int, fetchErr error) (*YearData, error) {
	t.lock()
	defer t.mu.Unlock()
	parts := make([]*YearData, 0, 12)
	for _, m := range monthsIn(monthOf(year, time.January, t.location()).start, monthOf(year, time.December, t.location()).end, t.location()) {
		e, ok := t.cache[t.monthKey(m)]
		if !ok {
			if fetchErr == nil {
				fetchErr = fmt.Errorf("toggl: %s: not fetched", t.monthKey(m))
			}
			return nil, fetchErr
		}
		parts = append(parts, e.val.(*YearData))
	}
	return mergeYearData(parts...), nil
}

func mergeYearData(parts ...*YearData) *YearData {
	type key struct {
		pid, rate int
		currency  string
		month     time.Month
	}
	acc := map[key]*Aggregate{}
	var order []key
	out := emptyYearData()
	for _, yd := range parts {
		for month, aggs := range yd.Months {
			for _, agg := range aggs {
				k := key{agg.ProjectID, agg.RateCents, agg.Currency, month}
				if acc[k] == nil {
					order = append(order, k)
				}
				acc[k] = addAggregate(acc[k], agg.ProjectID, agg.RateCents, agg.Currency, agg.AmountCents, agg.Seconds)
			}
		}
		for day := range yd.Days {
			out.Days[day] = true
		}
	}
	for _, k := range order {
		out.Months[k.month] = append(out.Months[k.month], *acc[k])
	}
	return out
}

func (t *Toggl) Projects(ctx context.Context) (map[int]Project, error) {
	if t == nil {
		return map[int]Project{}, nil
	}
	v, err := t.getCachedAs(ctx, "projects", kindProjects, time.Time{}, time.Time{}, func(fetchCtx context.Context) (any, error) {
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
		if err == nil {
			t.noteQuota(resp)
		}
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
		t.mu.Lock()
		t.counters.Retries++
		t.mu.Unlock()
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
	t.mu.Lock()
	t.counters.Requests++
	t.mu.Unlock()
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
	return &statusError{API: api, Status: resp.StatusCode, Body: strings.Join(strings.Fields(string(msg)), " ")}
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
