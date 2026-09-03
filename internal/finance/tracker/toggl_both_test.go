package tracker

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func detailedRowJSON(project, rateCents, seconds int, start string) string {
	return `{"project_id":` + itoa(project) + `,"hourly_rate_in_cents":` + itoa(rateCents) +
		`,"billable_amount_in_cents":` + itoa(seconds*rateCents/3600) + `,"currency":"EUR","time_entries":[{"seconds":` +
		itoa(seconds) + `,"start":"` + start + `T09:00:00+01:00"}]}`
}

func bothOver(b *fakeBackend) *Combined {
	client := b.transport()
	track := &Toggl{Token: "tok", WorkspaceID: "ws", HTTP: client}
	focus := NewFocus(FocusConfig{Key: "toggl_sk", OrganizationID: "10", WorkspaceID: "20"}, client)
	return Both(track, focus)
}

func TestBothSumsTheSameProjectAndKeepsDistinctOnes(t *testing.T) {
	b := &fakeBackend{
		detailed: func(int) (string, string, string) {
			return "[" + detailedRowJSON(1, 5000, 3600, "2026-03-10") + "]", "", ""
		},
		focus: &fakeFocus{
			entries:      onePage(entry("2026-03-11", 3600, 1), entry("2026-03-12", 3600, 2)),
			projectRates: map[int]string{1: rate50, 2: rate50},
		},
	}
	yd, err := bothOver(b).Year(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	march := map[int]Aggregate{}
	for _, a := range yd.Months[time.March] {
		march[a.ProjectID] = a
	}
	if len(march) != 2 {
		t.Fatalf("March = %+v, want projects 1 and 2", yd.Months[time.March])
	}
	if march[1].Seconds != 7200 || march[1].AmountCents != 10000 {
		t.Errorf("project 1 = %+v, want two hours for 100.00 (one from each API)", march[1])
	}
	if march[2].Seconds != 3600 || march[2].AmountCents != 5000 {
		t.Errorf("project 2 = %+v, want one hour for 50.00", march[2])
	}
	for _, day := range []string{"2026-03-10", "2026-03-11", "2026-03-12"} {
		if !yd.Days[day] {
			t.Errorf("Days lacks %s: %v", day, yd.Days)
		}
	}
}

func TestBothMergesProjectsWithTheNewerNameWinning(t *testing.T) {
	b := &fakeBackend{
		projects: `[{"id":1,"name":"Alpha","client_id":100},{"id":3,"name":"Gamma"}]`,
		focus:    &fakeFocus{projects: `[{"id":1,"name":"Alpha (2.0)","client_id":100},{"id":2,"name":"Beta"}]`},
	}
	got, err := bothOver(b).Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Name != "Alpha (2.0)" || got[2].Name != "Beta" || got[3].Name != "Gamma" {
		t.Errorf("Projects = %+v", got)
	}
}

func TestBothFailsTheYearWhenOneSideRejectsTheKey(t *testing.T) {
	b := &fakeBackend{focus: &fakeFocus{failEntries: http.StatusUnauthorized}}
	c := bothOver(b)
	if _, err := c.Year(context.Background(), 2026); err == nil {
		t.Fatal("expected the year to fail rather than report half the hours")
	}
	s := c.KeyStatus(time.Now())
	if !s.Rejected || !strings.Contains(s.Warning, "TOGGL2_API_KEY") {
		t.Errorf("KeyStatus = %+v, want the 2.0 rejection", s)
	}
	if c.Mode() != ModeBoth {
		t.Errorf("Mode = %q, want %q", c.Mode(), ModeBoth)
	}
}

func TestBothFansOutStalenessAndEviction(t *testing.T) {
	b := &fakeBackend{focus: &fakeFocus{}}
	c := bothOver(b)
	ctx := context.Background()
	if _, err := c.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if at, stale := c.YearStatus(2026); at.IsZero() || stale {
		t.Fatalf("YearStatus = %v/%v after a fetch, want fresh", at, stale)
	}
	if c.YearPending(2026) {
		t.Error("YearPending after a fetch")
	}

	c.markYearStale(2026)
	if _, stale := c.YearStatus(2026); !stale {
		t.Error("markYearStale did not reach both sides")
	}
	if _, err := c.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	c.EvictRange(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if _, stale := c.YearStatus(2026); !stale {
		t.Error("EvictRange did not reach both sides")
	}
	if _, trackStale := c.Track.YearStatus(2026); !trackStale {
		t.Error("Track side not evicted")
	}
	if _, focusStale := c.Focus.YearStatus(2026); !focusStale {
		t.Error("Focus side not evicted")
	}
}
