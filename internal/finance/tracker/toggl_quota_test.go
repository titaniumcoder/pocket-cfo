package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func quotaTransport(remaining, resetsIn string, exhausted *bool, calls *int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		h := map[string]string{"X-Toggl-Quota-Remaining": remaining, "X-Toggl-Quota-Resets-In": resetsIn}
		if exhausted != nil && *exhausted {
			resp := statusResponse(http.StatusPaymentRequired, `{"error":"quota exceeded"}`)
			resp.Header.Set("X-Toggl-Quota-Remaining", "0")
			resp.Header.Set("X-Toggl-Quota-Resets-In", resetsIn)
			return resp, nil
		}
		if strings.Contains(r.URL.Path, "/search/time_entries") {
			return jsonResponse(`[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":7500,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-03-02T09:00:00+00:00"}]}]`, h), nil
		}
		return jsonResponse(`[]`, h), nil
	})}
}

func TestQuotaHeadersAreRemembered(t *testing.T) {
	calls := 0
	tg := &Toggl{WorkspaceID: "ws", HTTP: quotaTransport("27", "1800", nil, &calls)}
	if _, err := tg.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	q := tg.Quota(now)
	if q.Remaining != 27 || q.Exhausted {
		t.Errorf("Quota = %+v, want 27 left and not exhausted", q)
	}
	if until := q.ResetAt.Sub(now); until < 29*time.Minute || until > 31*time.Minute {
		t.Errorf("ResetAt is %s away, want about 30 minutes", until)
	}
	if q := tg.Quota(now.Add(31 * time.Minute)); q.Remaining != -1 {
		t.Errorf("after the window resets Remaining = %d, want unknown (-1)", q.Remaining)
	}
}

func TestA402ClosesTheGateUntilTheWindowResets(t *testing.T) {
	defer withBreakerCooldown(time.Hour)()
	calls := 0
	exhausted := false
	tg := &Toggl{WorkspaceID: "ws", HTTP: quotaTransport("1", "120", &exhausted, &calls)}
	ctx := context.Background()
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.Projects(ctx); err != nil {
		t.Fatal(err)
	}
	before := calls

	exhausted = true
	tg.EvictRange(mar(1), mar(31))
	yd, err := tg.Year(ctx, 2026)
	if err != nil || yd == nil || len(yd.Months[time.March]) != 1 {
		t.Fatalf("a 402 with cached months must serve the stale year: %v / %+v", err, yd)
	}
	if calls != before+1 {
		t.Fatalf("made %d requests for the refresh, want exactly 1 (no retry on 402)", calls-before)
	}
	q := tg.Quota(time.Now())
	if !q.Exhausted || q.Remaining != 0 || !strings.Contains(q.Note, "refresh again at") {
		t.Errorf("Quota = %+v, want exhausted with a note", q)
	}
	if until := q.ResetAt.Sub(time.Now()); until < 2*time.Minute || until > 2*time.Minute+quotaGateSlack+time.Second {
		t.Errorf("gate is %s away, want the reset window plus a little slack", until)
	}

	exhausted = false
	tg.EvictRange(mar(1), mar(31))
	if _, err := tg.Year(ctx, 2026); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.Projects(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != before+1 {
		t.Errorf("made %d requests while the gate is closed, want none", calls-before-1)
	}
	if _, stale := tg.Status(mar(1), mar(31)); !stale {
		t.Error("March must still read stale while the quota keeps it from refreshing")
	}
	if len(tg.breaker) != 0 {
		t.Errorf("a 402 must not count toward the breaker, got %+v", tg.breaker)
	}
}

func TestA402WithNothingCachedIsAQuotaError(t *testing.T) {
	calls := 0
	exhausted := true
	tg := &Toggl{WorkspaceID: "ws", HTTP: quotaTransport("0", "60", &exhausted, &calls)}
	_, err := tg.Year(context.Background(), 2026)
	if !isQuotaExhausted(err) {
		t.Fatalf("err = %v, want the 402 recognised as quota exhaustion", err)
	}
	_, err = tg.Year(context.Background(), 2026)
	if !isQuotaExhausted(err) || calls != 1 {
		t.Errorf("second call: err = %v, calls = %d — want the gate to answer without a request", err, calls)
	}
}

func TestWarmSkipsATickWhenTheQuotaIsNearlyUsedUp(t *testing.T) {
	calls := 0
	client := quotaTransport("3", "600", nil, &calls)
	trk := &Tracker{Toggl: &Toggl{WorkspaceID: "ws", HTTP: client}, Holidays: &Holidays{HTTP: client}, Loc: time.UTC}
	if _, err := trk.Toggl.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	before := calls
	markYearStale(trk.Toggl, 2026)
	trk.warmOnce(context.Background(), time.Minute)
	if calls != before {
		t.Errorf("the warmer made %d requests with only 3 left in the quota, want none", calls-before)
	}
	if _, err := trk.Toggl.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	if calls == before {
		t.Error("a page view must still be allowed to spend the reserve")
	}
}

func TestComputeShowsTheQuotaNoteInsteadOfPolling(t *testing.T) {
	calls := 0
	exhausted := true
	client := quotaTransport("0", "900", &exhausted, &calls)
	trk := &Tracker{Toggl: &Toggl{WorkspaceID: "ws", HTTP: client}, Holidays: &Holidays{HTTP: client}, HoursPerDay: 8, Loc: time.UTC, RateCents: 7500, RateCurrency: "EUR"}

	f := trk.ComputeMonth(context.Background(), 2026, time.March)
	if f.TogglPending {
		t.Error("nothing is in flight behind a closed gate, so the page must not poll")
	}
	if !strings.Contains(f.TogglQuotaNote, "quota is used up") {
		t.Errorf("TogglQuotaNote = %q", f.TogglQuotaNote)
	}
	w := httptest.NewRecorder()
	RenderPage(w, f)
	if body := w.Body.String(); !strings.Contains(body, "quota is used up") || strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("the page must carry the quota note and no auto-refresh")
	}
}

func TestBothReportsTheTighterQuota(t *testing.T) {
	track := &Toggl{}
	focus := NewFocus(FocusConfig{Key: "k", OrganizationID: "1", WorkspaceID: "2"}, nil)
	now := time.Now()
	track.quotaKnown, track.quotaRemaining, track.quotaResetAt = true, 200, now.Add(time.Hour)
	focus.quotaKnown, focus.quotaRemaining, focus.quotaResetAt = true, 12, now.Add(time.Hour)
	c := Both(track, focus)
	if q := c.Quota(now); q.Remaining != 12 {
		t.Errorf("Remaining = %d, want the 2.0 side's 12", q.Remaining)
	}
	track.quotaGateUntil = now.Add(time.Minute)
	if q := c.Quota(now); !q.Exhausted {
		t.Error("an exhausted side must win")
	}
}
