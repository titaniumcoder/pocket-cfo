package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// newInfoTestServer builds a server with the real info.html template
// (resolved to an absolute path) and a network-free Tracker: Holidays hits
// a fake transport instead of the real openholidaysapi.org, Toggl stays nil
// (not configured) unless the caller sets it afterwards.
func newInfoTestServer(t *testing.T) *server {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	infoTmpl := mustPageTemplate(filepath.Join(wd, "..", "..", "templates", "info.html"))

	noNetwork := &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "openholidaysapi.org") {
			return jsonResp(http.StatusOK, `[]`), nil
		}
		t.Fatalf("unexpected request in a network-free info test: %s %s", r.Method, r.URL)
		return nil, nil
	})}

	return &server{
		cfg:      config{env: "prod", sessionSecret: "test-secret"},
		infoTmpl: infoTmpl,
		tracker:  &tracker.Tracker{Holidays: &tracker.Holidays{HTTP: noNetwork}},
	}
}

func authorizedRequest(t *testing.T, s *server) *http.Request {
	t.Helper()
	encoded, err := auth.Encode(s.cfg.sessionSecret, auth.NewSession("octocat", "push", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/info", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
	return r
}

func TestHandleInfo_ForbiddenForReadOnlySession(t *testing.T) {
	s := newInfoTestServer(t)
	r := readOnlyRequest(t, s, "/info", []string{"finance", "invoicing"})
	w := httptest.NewRecorder()

	s.handleInfo(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — /info is authorized()-only, even a full-part readonly session must not see it", w.Code)
	}
}

// TestHandleInfo_UnauthenticatedRedirectsToLogin pins the distinction
// between "we don't know who you are" and "we do, and it isn't enough":
// a logged-out visitor gets sent to log in, not a dead-end 403 on a page
// they may well be entitled to. The 403 case is
// TestHandleInfo_ForbiddenForReadOnlySession above.
func TestHandleInfo_UnauthenticatedRedirectsToLogin(t *testing.T) {
	s := newInfoTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/info", nil) // no cookie
	w := httptest.NewRecorder()

	s.handleInfo(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (redirect to login)", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/auth/login" {
		t.Errorf("Location = %q, want /auth/login", got)
	}
}

func TestHandleInfo_RendersForAuthorizedSession(t *testing.T) {
	s := newInfoTestServer(t)
	r := authorizedRequest(t, s)
	w := httptest.NewRecorder()

	s.handleInfo(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Not configured (TOGGL_API_TOKEN") {
		t.Error("expected the Toggl section to show 'not configured' (no Toggl wired into the test tracker)")
	}
}

// TestInfoTemplate_SectionOrder pins the page's section order: the two Toggl
// panels, Track before 2.0, then OpenHolidays.
func TestInfoTemplate_SectionOrder(t *testing.T) {
	s := newInfoTestServer(t)
	var buf bytes.Buffer
	if err := s.infoTmpl.Execute(&buf, infoView{}); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	toggl := strings.Index(body, ">Toggl Track<")
	toggl2 := strings.Index(body, ">Toggl 2.0<")
	holidays := strings.Index(body, "Holiday API (OpenHolidays)")
	if toggl < 0 || toggl2 < 0 || holidays < 0 {
		t.Fatalf("missing a section: toggl=%d toggl2=%d holidays=%d", toggl, toggl2, holidays)
	}
	if !(toggl < toggl2 && toggl2 < holidays) {
		t.Errorf("section order = toggl@%d toggl2@%d holidays@%d, want toggl < toggl2 < holidays", toggl, toggl2, holidays)
	}
	if strings.Contains(body, "api2pdf") {
		t.Error("the api2pdf balance panel is back; the server no longer holds that key")
	}
}

func TestHandleInfo_ShowsTheToggl2Panel(t *testing.T) {
	s := newInfoTestServer(t)
	yesterday := time.Now().AddDate(0, 0, -1)
	s.togglFocus = tracker.NewFocus(tracker.FocusConfig{Key: "toggl_sk_x", OrganizationID: "10", WorkspaceID: "20", KeyExpiresAt: yesterday}, focusTransport(t))
	s.cfg.togglMode = togglModeFocus
	s.cfg.finance.Toggl2Key = "toggl_sk_x"
	s.cfg.finance.Toggl2Organization = "10"
	s.cfg.finance.Toggl2Workspace = "20"
	s.cfg.finance.TogglMode = togglModeFocus
	s.tracker.Toggl = s.togglFocus

	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"Not configured (TOGGL_API_TOKEN",
		"Workspace 20",
		">Acme<",
		"Feeds the dashboard",
		"expired on " + yesterday.Format("02 Jan 2006"),
		"TOGGL2_API_KEY",
		"toggl2 — Toggl 2.0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "toggl_sk_x") {
		t.Error("the 2.0 key is printed in clear")
	}
}

func TestHandleInfo_RendersTheRulesTimeline(t *testing.T) {
	s := newInfoTestServer(t)
	legislation, err := tracker.ParseLegislation([]tracker.LegislationEntry{
		{From: "2026-01", DividendTax: &tracker.TaxEntry{Bands: []tracker.BandEntry{{From: 0, Rate: ratePtr(0.05)}}}},
		{From: "2026-07", MinimumWage: ratePtr(1077)},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.tracker.Personal = tracker.PersonalParams{Legislation: legislation}

	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`href="#rules-2026-01"`,
		`id="rules-2026-07"`,
		"From July 2026",
		"<span class=\"since\">since January 2026</span>",
		"1,077",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "No dated rules configured") {
		t.Error("the empty state shows although legislation is configured")
	}
}

func focusTransport(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "focus.toggl.com" && strings.HasSuffix(r.URL.Path, "/clients") {
			return jsonResp(http.StatusOK, `{"data":[{"id":5,"name":"Acme"}],"page":1,"per_page":50}`), nil
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		return nil, nil
	})}
}

func TestHandleInfo_KeyOnlyToggl2SaysWhereTheIdsAre(t *testing.T) {
	s := newInfoTestServer(t)
	s.togglFocus = tracker.NewFocus(tracker.FocusConfig{Key: "toggl_sk_x"}, &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("a key-only panel must make no request, got %s %s", r.Method, r.URL)
		return nil, nil
	})})
	s.cfg.finance.Toggl2Key = "toggl_sk_x"

	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Only <code>TOGGL2_API_KEY</code> is set", "/organizations/&lt;organization id&gt;/workspaces/&lt;workspace id&gt;/"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, unwanted := range []string{"Feeds the dashboard", "Not configured (TOGGL2_API_KEY", "Seen by the key", "status 403"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("page shows %q", unwanted)
		}
	}
}
