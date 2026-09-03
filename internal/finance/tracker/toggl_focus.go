package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FocusConfig struct {
	Key            string
	OrganizationID string
	WorkspaceID    string
	ProjectIDs     string
	KeyExpiresAt   time.Time
}

func NewFocus(cfg FocusConfig, httpClient *http.Client) *Toggl {
	t := &Toggl{HTTP: httpClient, keyExpiresAt: cfg.KeyExpiresAt}
	t.api = &focusAPI{t: t, cfg: cfg, projects: idSet(parseIDs(cfg.ProjectIDs))}
	return t
}

var focusBaseURL = "https://focus.toggl.com/api"

const (
	focusPerPage    = 50
	focusMinPerPage = 10
	focusMaxPages   = 100
)

var (
	everSince = time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
	everUntil = time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
)

type focusAPI struct {
	t        *Toggl
	cfg      FocusConfig
	projects map[int]bool

	mu       sync.Mutex
	pageSize map[string]int
}

func (a *focusAPI) perPage(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n, ok := a.pageSize[path]; ok {
		return n
	}
	return focusPerPage
}

func (a *focusAPI) shrinkPage(path string, current int) bool {
	next := current / 2
	if next < focusMinPerPage {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pageSize == nil {
		a.pageSize = map[string]int{}
	}
	a.pageSize[path] = next
	return true
}

func isPageTooLarge(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.Status == http.StatusBadRequest && strings.Contains(se.Body, "PerPage")
}

func idSet(ids []int) map[int]bool {
	set := make(map[int]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func (a *focusAPI) mode() Mode {
	return ModeFocus
}

func (a *focusAPI) keyVar() string {
	return "TOGGL2_API_KEY"
}

func (a *focusAPI) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.cfg.Key)
}

func (a *focusAPI) cacheScope() string {
	return "focus|" + a.cfg.ProjectIDs
}

func (a *focusAPI) workspacePath(rest string) string {
	return "/organizations/" + a.cfg.OrganizationID + "/workspaces/" + a.cfg.WorkspaceID + "/" + rest
}

func (a *focusAPI) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	u := focusBaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	resp, err := a.t.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError("toggl2", resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("toggl2: decode %s: %w", path, err)
	}
	return nil
}

func focusPages[T any](ctx context.Context, a *focusAPI, path string, q url.Values) ([]T, error) {
	var all []T
	size := a.perPage(path)
	for page := 1; page <= focusMaxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(size))
		var body struct {
			Data []T `json:"data"`
		}
		err := a.getJSON(ctx, path, q, &body)
		if isPageTooLarge(err) && a.shrinkPage(path, size) {
			size = a.perPage(path)
			all, page = nil, 0
			continue
		}
		if err != nil {
			return nil, err
		}
		all = append(all, body.Data...)
		if len(body.Data) < size {
			break
		}
	}
	return all, nil
}

type focusEntry struct {
	Start     string `json:"start"`
	Duration  int    `json:"duration"`
	Billable  bool   `json:"billable"`
	ProjectID *int   `json:"project_id"`
	Type      string `json:"type"`
	DeletedAt string `json:"deleted_at"`
}

func (a *focusAPI) counts(e focusEntry) bool {
	if e.Type != "activity" || !e.Billable || e.DeletedAt != "" || e.Duration <= 0 || len(e.Start) < 10 {
		return false
	}
	return len(a.projects) == 0 || a.projects[derefInt(e.ProjectID)]
}

func roundToQuarterHour(seconds int) int {
	const quarter = roundingMinutes * 60
	return (seconds + quarter/2) / quarter * quarter
}

func entryAmountCents(seconds, rateCents int) int {
	return int(math.Round(float64(seconds) * float64(rateCents) / 3600))
}

func (a *focusAPI) fetchRange(ctx context.Context, start, end time.Time) (*YearData, error) {
	q := url.Values{
		"date_from":        {start.Format(time.RFC3339)},
		"date_to":          {end.Add(24*time.Hour - time.Second).Format(time.RFC3339)},
		"include_taskless": {"true"},
	}
	entries, err := focusPages[focusEntry](ctx, a, a.workspacePath("time-entries"), q)
	if err != nil {
		return nil, err
	}

	type key struct {
		pid, rate int
		month     time.Month
	}
	acc := map[key]*Aggregate{}
	days := map[string]bool{}
	for _, e := range entries {
		if !a.counts(e) {
			continue
		}
		sec := roundToQuarterHour(e.Duration)
		if sec == 0 {
			continue
		}
		day := e.Start[:10]
		days[day] = true
		pid := derefInt(e.ProjectID)
		rate, currency, err := a.rateOn(ctx, pid, day)
		if err != nil {
			return nil, err
		}
		month := start.Month()
		if tm, perr := time.Parse("2006-01-02", day); perr == nil {
			month = tm.Month()
		}
		k := key{pid, rate, month}
		acc[k] = addAggregate(acc[k], pid, rate, currency, entryAmountCents(sec, rate), sec)
	}

	yd := &YearData{Months: map[time.Month][]Aggregate{}, Days: days}
	for k, agg := range acc {
		yd.Months[k.month] = append(yd.Months[k.month], *agg)
	}
	return yd, nil
}

type rateSpan struct {
	HourlyRate int    `json:"hourly_rate"`
	Currency   string `json:"currency"`
	StartDate  string `json:"start_date"`
	EndDate    string `json:"end_date"`
}

func (r rateSpan) covers(day string) bool {
	return (r.StartDate == "" || dayOf(r.StartDate) <= day) && (r.EndDate == "" || day <= dayOf(r.EndDate))
}

func dayOf(timestamp string) string {
	if len(timestamp) > 10 {
		return timestamp[:10]
	}
	return timestamp
}

func rateFor(spans []rateSpan, day string) (rateSpan, bool) {
	for _, r := range spans {
		if r.covers(day) {
			return r, true
		}
	}
	return rateSpan{}, false
}

func (a *focusAPI) rateOn(ctx context.Context, projectID int, day string) (rateCents int, currency string, err error) {
	if projectID != 0 {
		spans, err := a.projectRates(ctx, projectID)
		if err != nil {
			return 0, "", err
		}
		if r, ok := rateFor(spans, day); ok {
			return r.HourlyRate, r.Currency, nil
		}
	}
	spans, err := a.workspaceRates(ctx)
	if err != nil {
		return 0, "", err
	}
	if r, ok := rateFor(spans, day); ok {
		return r.HourlyRate, r.Currency, nil
	}
	return 0, "", nil
}

func (a *focusAPI) projectRates(ctx context.Context, projectID int) ([]rateSpan, error) {
	id := strconv.Itoa(projectID)
	return a.cachedRates(ctx, "rates|project|"+id, a.workspacePath("billable-rates/projects/"+id+"/rates"))
}

func (a *focusAPI) workspaceRates(ctx context.Context) ([]rateSpan, error) {
	return a.cachedRates(ctx, "rates|workspace", a.workspacePath("billable-rates/workspace/rates"))
}

func (a *focusAPI) cachedRates(ctx context.Context, key, path string) ([]rateSpan, error) {
	v, err := a.t.getCachedAs(ctx, key, kindRates, everSince, everUntil, func(fetchCtx context.Context) (any, error) {
		var spans []rateSpan
		err := a.getJSON(fetchCtx, path, nil, &spans)
		if isNotFound(err) {
			return []rateSpan{}, nil
		}
		return spans, err
	})
	if err != nil {
		return nil, err
	}
	return v.([]rateSpan), nil
}

func isNotFound(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound
}

type focusProject struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	ClientID *int   `json:"client_id"`
}

func (a *focusAPI) fetchProjects(ctx context.Context) (map[int]Project, error) {
	list, err := focusPages[focusProject](ctx, a, a.workspacePath("projects"), url.Values{"archived": {""}})
	if err != nil {
		return nil, err
	}
	out := make(map[int]Project, len(list))
	for _, p := range list {
		out[p.ID] = Project{Name: p.Name, ClientID: derefInt(p.ClientID)}
	}
	return out, nil
}

func (a *focusAPI) fetchWorkspaces(context.Context) ([]Workspace, error) {
	id, err := strconv.Atoi(a.cfg.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("toggl2: TOGGL2_WORKSPACE_ID=%q is not a number", a.cfg.WorkspaceID)
	}
	return []Workspace{{ID: id, Name: "Workspace " + a.cfg.WorkspaceID}}, nil
}

type focusClient struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (a *focusAPI) fetchClients(ctx context.Context, workspaceID int) ([]Client, error) {
	list, err := focusPages[focusClient](ctx, a, "/workspaces/"+strconv.Itoa(workspaceID)+"/clients", url.Values{})
	if err != nil {
		return nil, err
	}
	out := make([]Client, len(list))
	for i, c := range list {
		out[i] = Client{ID: c.ID, Name: c.Name}
	}
	return out, nil
}
