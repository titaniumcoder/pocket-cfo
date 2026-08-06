package tracker

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

func TestProjects(t *testing.T) {
	b := &fakeBackend{projects: `[{"id":1,"name":"Alpha"},{"id":2,"name":"Beta"}]`}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}

	got, err := tg.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Name != "Alpha" || got[2].Name != "Beta" {
		t.Errorf("projects = %+v", got)
	}
}

func TestProjectsErrorStatus(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return statusResponse(401, "unauthorized"), nil
	})
	tg := &Toggl{WorkspaceID: "ws", HTTP: &http.Client{Transport: rt}}
	if _, err := tg.Projects(context.Background()); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestYearAggregatesPerProjectAndRate(t *testing.T) {
	// Two March rows for the same project+rate are summed; a different rate is its
	// own group; a February row lands in a different month bucket.
	body := `[
	  {"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":15000,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-03-02T09:00:00+00:00"}]},
	  {"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":7500,"currency":"EUR","time_entries":[{"seconds":1800,"start":"2026-03-03T09:00:00+00:00"}]},
	  {"project_id":2,"hourly_rate_in_cents":9000,"billable_amount_in_cents":9000,"currency":"USD","time_entries":[{"seconds":3600,"start":"2026-03-04T09:00:00+00:00"}]},
	  {"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":3000,"currency":"EUR","time_entries":[{"seconds":1200,"start":"2026-02-10T09:00:00+00:00"}]}
	]`
	b := &fakeBackend{detailed: func(page int) (string, string, string) { return body, "", "" }}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}

	yd, err := tg.Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}

	march := yd.Months[3]
	sort.Slice(march, func(i, j int) bool { return march[i].ProjectID < march[j].ProjectID })
	if len(march) != 2 {
		t.Fatalf("March groups = %d, want 2: %+v", len(march), march)
	}
	if a := march[0]; a.ProjectID != 1 || a.RateCents != 7500 || a.AmountCents != 22500 || a.Seconds != 5400 || a.Currency != "EUR" {
		t.Errorf("March project1 = %+v", a)
	}
	if a := march[1]; a.ProjectID != 2 || a.AmountCents != 9000 || a.Currency != "USD" {
		t.Errorf("March project2 = %+v", a)
	}

	feb := yd.Months[2]
	if len(feb) != 1 || feb[0].AmountCents != 3000 || feb[0].Seconds != 1200 {
		t.Errorf("Feb groups = %+v, want one row of 3000c/1200s", feb)
	}
}

func TestYearPaginates(t *testing.T) {
	page0 := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":7500,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-03-02T09:00:00+00:00"}]}]`
	page1 := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":7500,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-03-03T09:00:00+00:00"}]}]`
	b := &fakeBackend{detailed: func(page int) (string, string, string) {
		if page == 0 {
			return page0, "51", "999" // signal a next page
		}
		return page1, "", "" // last page
	}}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}

	yd, err := tg.Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	march := yd.Months[3]
	if len(march) != 1 || march[0].Seconds != 7200 || march[0].AmountCents != 15000 {
		t.Errorf("paginated March = %+v, want summed across two pages", march)
	}
}

// TestEachDetailedRowFollowUpOmitsFirstID guards against a real bug found
// against Toggl's live API: including first_id (from the X-Next-ID response
// header) alongside first_row_number on a follow-up page makes Toggl
// silently return zero rows despite the first page's header claiming more
// data exists. A response-simulating fake (like TestYearPaginates) can't
// catch this — it returns canned data regardless of what was requested —
// so this test inspects the actual follow-up request body instead.
func TestEachDetailedRowFollowUpOmitsFirstID(t *testing.T) {
	page0 := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":7500,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-03-02T09:00:00+00:00"}]}]`
	var secondBody string
	call := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		call++
		if call == 1 {
			return jsonResponse(page0, map[string]string{"X-Next-Row-Number": "51", "X-Next-ID": "999"}), nil
		}
		secondBody = string(body)
		return jsonResponse(`[]`, nil), nil
	})
	tg := &Toggl{WorkspaceID: "ws", HTTP: &http.Client{Transport: rt}}

	if _, err := tg.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	if call != 2 {
		t.Fatalf("expected 2 requests, got %d", call)
	}
	if strings.Contains(secondBody, "first_id") {
		t.Errorf("follow-up request body contains first_id, want it omitted: %s", secondBody)
	}
	if !strings.Contains(secondBody, "first_row_number") {
		t.Errorf("follow-up request body missing first_row_number: %s", secondBody)
	}
}

func TestYearCaches(t *testing.T) {
	calls := 0
	b := &fakeBackend{detailed: func(page int) (string, string, string) {
		calls++
		return `[]`, "", ""
	}}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}

	for range 3 {
		if _, err := tg.Year(context.Background(), 2026); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("detailed endpoint hit %d times, want 1 (cached)", calls)
	}
}

func TestYearErrorStatus(t *testing.T) {
	b := &fakeBackend{failDetailed: 500}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}
	if _, err := tg.Year(context.Background(), 2026); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestYearBillableDays(t *testing.T) {
	body := `[
	  {"project_id":1,"time_entries":[{"seconds":3600,"start":"2026-03-02T09:00:00+00:00"},{"seconds":0,"start":"2026-03-03T09:00:00+00:00"}]},
	  {"project_id":2,"time_entries":[{"seconds":1800,"start":"2026-03-04T09:00:00+00:00"},{"seconds":600,"start":"short"}]}
	]`
	b := &fakeBackend{detailed: func(page int) (string, string, string) { return body, "", "" }}
	tg := &Toggl{WorkspaceID: "ws", HTTP: b.transport()}

	yd, err := tg.Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-03-02", "2026-03-04"} {
		if !yd.Days[d] {
			t.Errorf("expected %s in billable days", d)
		}
	}
	// Zero-seconds and malformed-start entries are excluded.
	if yd.Days["2026-03-03"] || yd.Days["short"] {
		t.Errorf("zero-seconds / malformed entries should be excluded: %v", yd.Days)
	}
}

func TestParseIDs(t *testing.T) {
	got := parseIDs(" 1, 2 ,,3 ,x ")
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("parseIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseIDs = %v, want %v", got, want)
		}
	}
	if parseIDs("") != nil {
		t.Errorf("parseIDs(\"\") should be nil")
	}
}

func TestDerefInt(t *testing.T) {
	if derefInt(nil) != 0 {
		t.Error("derefInt(nil) != 0")
	}
	n := 5
	if derefInt(&n) != 5 {
		t.Error("derefInt(&5) != 5")
	}
}

func TestYearKey(t *testing.T) {
	tg := &Toggl{ProjectIDs: "1,2"}
	if got, want := tg.yearKey(2026), "detailed|1,2|2026"; got != want {
		t.Errorf("yearKey = %q, want %q", got, want)
	}
}
