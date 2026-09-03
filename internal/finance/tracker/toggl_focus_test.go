package tracker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func focusToggl(f *fakeFocus, projectIDs string) *Toggl {
	b := &fakeBackend{focus: f}
	return NewFocus(FocusConfig{Key: "toggl_sk_test", OrganizationID: "10", WorkspaceID: "20", ProjectIDs: projectIDs}, b.transport())
}

func entry(start string, seconds, project int) string {
	return entryWith(start, seconds, project, "")
}

func entryWith(start string, seconds, project int, extra string) string {
	return fmt.Sprintf(`{"id":1,"start":"%sT09:00:00+02:00","duration":%d,"billable":true,"project_id":%d,"type":"activity"%s}`, start, seconds, project, extra)
}

func entriesPage(entries ...string) string {
	return "[" + strings.Join(entries, ",") + "]"
}

func onePage(entries ...string) func(int) string {
	return func(int) string { return entriesPage(entries...) }
}

const rate50 = `[{"id":1,"hourly_rate":5000,"currency":"EUR","start_date":"2026-01-01","end_date":null}]`

func TestFocusSendsBearerAuth(t *testing.T) {
	var got string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Get("Authorization")
		return jsonResponse(`{"data":[],"page":1,"per_page":200}`, nil), nil
	})
	tg := NewFocus(FocusConfig{Key: "toggl_sk_abc", OrganizationID: "1", WorkspaceID: "2"}, &http.Client{Transport: rt})

	if _, err := tg.Projects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer toggl_sk_abc" {
		t.Errorf("Authorization = %q, want Bearer toggl_sk_abc", got)
	}
}

func TestFocusYearRoundsEachEntryToTheNearestQuarterHour(t *testing.T) {
	f := &fakeFocus{
		entries: onePage(
			entry("2026-03-02", 7*60, 1),
			entry("2026-03-03", 8*60, 1),
			entry("2026-03-04", 22*60, 1),
			entry("2026-03-05", 23*60, 1),
		),
		projectRates: map[int]string{1: rate50},
	}
	yd, err := focusToggl(f, "").Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	aggs := yd.Months[time.March]
	if len(aggs) != 1 {
		t.Fatalf("March aggregates = %+v, want one", aggs)
	}
	if aggs[0].Seconds != 3600 {
		t.Errorf("Seconds = %d, want 3600 (0 + 15 + 15 + 30 minutes)", aggs[0].Seconds)
	}
	if aggs[0].AmountCents != 5000 || aggs[0].RateCents != 5000 || aggs[0].Currency != "EUR" {
		t.Errorf("aggregate = %+v, want one hour at 50.00 EUR", aggs[0])
	}
	if yd.Days["2026-03-02"] || !yd.Days["2026-03-03"] || !yd.Days["2026-03-05"] {
		t.Errorf("Days = %v, want the 7-minute entry's day left out and the others in", yd.Days)
	}
}

func TestFocusYearPricesEachEntryAtTheRateInForceOnItsDay(t *testing.T) {
	f := &fakeFocus{
		entries: onePage(entry("2026-03-10", 3600, 1), entry("2026-08-10", 3600, 1)),
		projectRates: map[int]string{1: `[
			{"id":1,"hourly_rate":5000,"currency":"EUR","start_date":"2026-01-01","end_date":"2026-06-30"},
			{"id":2,"hourly_rate":6000,"currency":"EUR","start_date":"2026-07-01","end_date":null}]`},
	}
	yd, err := focusToggl(f, "").Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := yd.Months[time.March]; len(got) != 1 || got[0].RateCents != 5000 || got[0].AmountCents != 5000 {
		t.Errorf("March = %+v, want an hour at 50.00", got)
	}
	if got := yd.Months[time.August]; len(got) != 1 || got[0].RateCents != 6000 || got[0].AmountCents != 6000 {
		t.Errorf("August = %+v, want an hour at 60.00", got)
	}
}

func TestFocusYearFallsBackToTheWorkspaceRate(t *testing.T) {
	f := &fakeFocus{
		entries:        onePage(entry("2026-03-10", 1800, 1)),
		workspaceRates: `[{"id":9,"hourly_rate":4000,"currency":"CHF","start_date":"2020-01-01"}]`,
	}
	yd, err := focusToggl(f, "").Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := yd.Months[time.March]; len(got) != 1 || got[0].RateCents != 4000 || got[0].AmountCents != 2000 || got[0].Currency != "CHF" {
		t.Errorf("March = %+v, want half an hour at the workspace's 40.00 CHF", got)
	}
}

func TestFocusYearWithoutAnyRateKeepsTheHours(t *testing.T) {
	f := &fakeFocus{entries: onePage(entry("2026-03-10", 3600, 1))}
	yd, err := focusToggl(f, "").Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := yd.Months[time.March]; len(got) != 1 || got[0].Seconds != 3600 || got[0].RateCents != 0 || got[0].AmountCents != 0 {
		t.Errorf("March = %+v, want the hour with no rate and no amount", got)
	}
}

func TestFocusYearKeepsOnlyBillableActivities(t *testing.T) {
	f := &fakeFocus{
		entries: onePage(
			entry("2026-03-10", 3600, 1),
			strings.Replace(entry("2026-03-11", 3600, 1), `"type":"activity"`, `"type":"break"`, 1),
			strings.Replace(entry("2026-03-12", 3600, 1), `"billable":true`, `"billable":false`, 1),
			entryWith("2026-03-13", 3600, 1, `,"deleted_at":"2026-03-14T10:00:00Z"`),
			entry("2026-03-14", 0, 1),
		),
		projectRates: map[int]string{1: rate50},
	}
	yd, err := focusToggl(f, "").Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := yd.Months[time.March]; len(got) != 1 || got[0].Seconds != 3600 {
		t.Errorf("March = %+v, want only the billable activity's hour", got)
	}
	if len(yd.Days) != 1 || !yd.Days["2026-03-10"] {
		t.Errorf("Days = %v, want only 2026-03-10", yd.Days)
	}
}

func TestFocusYearHonoursTheProjectList(t *testing.T) {
	f := &fakeFocus{
		entries:      onePage(entry("2026-03-10", 3600, 1), entry("2026-03-11", 3600, 2)),
		projectRates: map[int]string{1: rate50, 2: rate50},
	}
	yd, err := focusToggl(f, "1").Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := yd.Months[time.March]; len(got) != 1 || got[0].ProjectID != 1 {
		t.Errorf("March = %+v, want project 1 only", got)
	}
	if f.callsTo("/billable-rates/projects/2/") != 0 {
		t.Error("fetched rates for a project outside the list")
	}
}

func TestFocusYearPaginatesUntilAShortPage(t *testing.T) {
	full := make([]string, focusPerPage)
	for i := range full {
		full[i] = entry("2026-03-10", 900, 1)
	}
	f := &fakeFocus{
		entries: func(n int) string {
			if n == 1 {
				return entriesPage(full...)
			}
			return entriesPage(entry("2026-03-11", 900, 1))
		},
		projectRates: map[int]string{1: rate50},
	}
	yd, err := focusToggl(f, "").Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if got := yd.Months[time.March]; len(got) != 1 || got[0].Seconds != (focusPerPage+1)*900 {
		t.Errorf("March = %+v, want %d quarter hours", got, focusPerPage+1)
	}
	if n := f.callsTo("/time-entries?"); n != 2 {
		t.Errorf("made %d time-entries requests, want 2", n)
	}
}

func TestFocusYearAsksForTheWholeYear(t *testing.T) {
	f := &fakeFocus{}
	if _, err := focusToggl(f, "").Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	call := f.calls[0]
	for _, want := range []string{"/organizations/10/workspaces/20/time-entries?", "date_from=2026-01-01T00%3A00%3A00Z", "date_to=2026-12-31T23%3A59%3A59Z", "include_taskless=true", "per_page=50"} {
		if !strings.Contains(call, want) {
			t.Errorf("request %q lacks %q", call, want)
		}
	}
}

func TestFocusRatesAreFetchedOncePerProjectAndAgainAfterReload(t *testing.T) {
	f := &fakeFocus{
		entries:      onePage(entry("2026-03-10", 3600, 1), entry("2026-03-11", 3600, 1), entry("2026-04-11", 3600, 1)),
		projectRates: map[int]string{1: rate50},
	}
	tg := focusToggl(f, "")
	ctx := context.Background()
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if n := f.callsTo("/billable-rates/projects/1/"); n != 1 {
		t.Fatalf("first year fetch made %d rate requests, want 1", n)
	}

	tg.markYearStale(2026)
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if n := f.callsTo("/billable-rates/projects/1/"); n != 1 {
		t.Errorf("a background refresh refetched the rates (%d requests)", n)
	}

	tg.EvictRange(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if n := f.callsTo("/billable-rates/projects/1/"); n != 2 {
		t.Errorf("Reload did not refetch the rates (%d requests)", n)
	}
}

func TestFocusProjectsIncludeArchived(t *testing.T) {
	f := &fakeFocus{projects: `[{"id":1,"name":"Alpha","client_id":100},{"id":2,"name":"Beta","client_id":null,"archived_at":"2026-01-01T00:00:00Z"}]`}
	got, err := focusToggl(f, "").Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Name != "Alpha" || got[1].ClientID != 100 || got[2].Name != "Beta" || got[2].ClientID != 0 {
		t.Errorf("Projects = %+v", got)
	}
	if !strings.Contains(f.calls[0], "/organizations/10/workspaces/20/projects?archived=&") {
		t.Errorf("request %q does not ask for archived projects too", f.calls[0])
	}
}

func TestFocusWorkspacesIsTheConfiguredOne(t *testing.T) {
	f := &fakeFocus{}
	got, err := focusToggl(f, "").Workspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 20 || got[0].Name != "Workspace 20" {
		t.Errorf("Workspaces = %+v, want the configured workspace 20", got)
	}
	if len(f.calls) != 0 {
		t.Errorf("listing workspaces made requests: %v", f.calls)
	}
}

func TestFocusClientsPaginate(t *testing.T) {
	f := &fakeFocus{clients: `[{"id":5,"name":"Acme","active":true}]`}
	got, err := focusToggl(f, "").Clients(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 5 || got[0].Name != "Acme" {
		t.Errorf("Clients = %+v", got)
	}
	if !strings.HasPrefix(f.calls[0], "/api/workspaces/20/clients?page=1&per_page=50") {
		t.Errorf("request = %q", f.calls[0])
	}
}

func TestFocusYearKeyIsScopedByMode(t *testing.T) {
	if got := focusToggl(&fakeFocus{}, "1,2").yearKey(2026); got != "focus|1,2|2026" {
		t.Errorf("yearKey = %q, want focus|1,2|2026", got)
	}
}

func TestFocusModeReportsItself(t *testing.T) {
	if got := focusToggl(&fakeFocus{}, "").Mode(); got != ModeFocus {
		t.Errorf("Mode = %q, want %q", got, ModeFocus)
	}
	if got := (&Toggl{}).Mode(); got != ModeTrack {
		t.Errorf("Track Mode = %q, want %q", got, ModeTrack)
	}
	var none *Toggl
	if got := none.Mode(); got != ModeOff {
		t.Errorf("nil Mode = %q, want %q", got, ModeOff)
	}
}

func TestFocusRejectedKeyIsRemembered(t *testing.T) {
	f := &fakeFocus{failEntries: http.StatusUnauthorized}
	tg := focusToggl(f, "")
	if _, err := tg.Year(context.Background(), 2026); err == nil {
		t.Fatal("expected the 401 to fail the year")
	}
	s := tg.KeyStatus(time.Now())
	if !s.Rejected || !strings.Contains(s.Warning, "TOGGL2_API_KEY") {
		t.Errorf("KeyStatus = %+v, want a rejection naming TOGGL2_API_KEY", s)
	}
}

func TestFocusShrinksThePageWhenTogglRefusesTheSize(t *testing.T) {
	f := &fakeFocus{clients: `[{"id":5,"name":"Acme"}]`, maxPerPage: 25}
	tg := focusToggl(f, "")
	got, err := tg.Clients(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Acme" {
		t.Errorf("Clients = %+v", got)
	}
	if len(f.calls) != 2 || !strings.Contains(f.calls[0], "per_page=50") || !strings.Contains(f.calls[1], "per_page=25") {
		t.Errorf("calls = %v, want 50 refused then 25 accepted", f.calls)
	}

}

func TestFocusRemembersTheWorkingPageSize(t *testing.T) {
	f := &fakeFocus{projects: `[{"id":1,"name":"Alpha"}]`, maxPerPage: 25}
	tg := focusToggl(f, "")
	if _, err := tg.Projects(context.Background()); err != nil {
		t.Fatal(err)
	}
	api := tg.api.(*focusAPI)
	if got := api.perPage(api.workspacePath("projects")); got != 25 {
		t.Errorf("remembered page size = %d, want 25", got)
	}
	if got := api.perPage(api.workspacePath("time-entries")); got != focusPerPage {
		t.Errorf("another endpoint's page size = %d, want the default %d", got, focusPerPage)
	}
}

func TestFocusGivesUpShrinkingBelowTheMinimum(t *testing.T) {
	f := &fakeFocus{clients: `[]`, maxPerPage: 1}
	if _, err := focusToggl(f, "").Clients(context.Background(), 20); err == nil || !strings.Contains(err.Error(), "PerPage") {
		t.Errorf("err = %v, want Toggl's validation error once no smaller page is left to try", err)
	}
}
