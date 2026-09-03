package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func yearBounds(year int) (time.Time, time.Time) {
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(1, 0, -1)
}

func togglWithACachedYear(t *testing.T) *tracker.Toggl {
	t.Helper()
	client := &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, `[]`), nil
	})}
	tg := &tracker.Toggl{WorkspaceID: "ws", HTTP: client}
	if _, err := tg.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	return tg
}

func yearCached(tg *tracker.Toggl) bool {
	start, end := yearBounds(2026)
	at, _ := tg.Status(start, end)
	return !at.IsZero()
}

func TestHandleTogglReset_ClearsBothBackendsForAnAuthorizedSession(t *testing.T) {
	s := newInfoTestServer(t)
	s.togglTrack = togglWithACachedYear(t)
	s.togglFocus = nil
	r := authorizedRequest(t, s)
	r.Method = http.MethodPost
	r.URL.Path = "/toggl/reset"
	w := httptest.NewRecorder()

	s.handleTogglReset(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/info" {
		t.Fatalf("status = %d location = %q, want a redirect back to /info", w.Code, w.Header().Get("Location"))
	}
	if yearCached(s.togglTrack) {
		t.Error("the Track cache survived the reset")
	}
}

func TestHandleTogglReset_RefusesReadOnlyAndAnonymous(t *testing.T) {
	s := newInfoTestServer(t)
	s.togglTrack = togglWithACachedYear(t)

	r := readOnlyRequest(t, s, "/toggl/reset", []string{"finance", "invoicing"})
	r.Method = http.MethodPost
	w := httptest.NewRecorder()
	s.handleTogglReset(w, r)
	if w.Code != http.StatusForbidden || !yearCached(s.togglTrack) {
		t.Errorf("readonly: status = %d cached = %v, want 403 and the cache untouched", w.Code, yearCached(s.togglTrack))
	}

	w = httptest.NewRecorder()
	s.handleTogglReset(w, httptest.NewRequest(http.MethodPost, "/toggl/reset", nil))
	if w.Code != http.StatusFound || !yearCached(s.togglTrack) {
		t.Errorf("anonymous: status = %d cached = %v, want a login redirect and the cache untouched", w.Code, yearCached(s.togglTrack))
	}
}

func TestInfoPageOffersTheResetOnlyWhenTogglIsConfigured(t *testing.T) {
	s := newInfoTestServer(t)
	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	if strings.Contains(w.Body.String(), "/toggl/reset") {
		t.Error("the reset form shows without any Toggl configured")
	}

	s.togglTrack = togglWithACachedYear(t)
	w = httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	if body := w.Body.String(); !strings.Contains(body, `action="/toggl/reset"`) || !strings.Contains(body, "Reset Toggl cache") {
		t.Error("the reset form is missing with Toggl Track configured")
	}
}

func TestInfoPageShowsCacheStatisticsPerConfiguredBackend(t *testing.T) {
	s := newInfoTestServer(t)
	s.togglTrack = togglWithACachedYear(t)
	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	body := w.Body.String()
	for _, want := range []string{"Months cached</td><td>12 (0 stale", "HTTP requests to Toggl</td><td>2, 0 of them retries", "none — memory only (TOGGL_CACHE_DIR unset)"} {
		if !strings.Contains(body, want) {
			t.Errorf("info page lacks %q", want)
		}
	}
	if strings.Contains(body, "Hourly quota") {
		t.Error("an API that never reported a quota must not get a quota row")
	}
	if strings.Count(body, `<h3 class="config-group">Toggl 2.0</h3>`) != 0 {
		t.Error("statistics shown for a backend that is not configured")
	}
}

func TestInfoPageShowsTheQuotaRowOnceTogglReportsOne(t *testing.T) {
	s := newInfoTestServer(t)
	client := &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := jsonResp(http.StatusOK, `[]`)
		resp.Header.Set("X-Toggl-Quota-Remaining", "21")
		resp.Header.Set("X-Toggl-Quota-Resets-In", "1200")
		return resp, nil
	})}
	s.togglTrack = &tracker.Toggl{WorkspaceID: "ws", HTTP: client}
	if _, err := s.togglTrack.Year(context.Background(), 2026); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	if body := w.Body.String(); !strings.Contains(body, "Hourly quota</td><td>21 requests left") {
		t.Error("the quota row is missing although Toggl reported 21 requests left")
	}
}
