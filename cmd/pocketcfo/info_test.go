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
	"github.com/titaniumcoder/pocket-cfo/internal/render"
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
	if !strings.Contains(body, "Not configured (API2PDF_KEY") {
		t.Error("expected the api2pdf section to show 'not configured' (no key in test config)")
	}
}

// TestInfoTemplate_SectionOrderAndBalanceFormatting pins the page's section
// order (api2pdf, then Toggl, then OpenHolidays) and the balance figure's
// formatting — two decimals, Bulgarian/European decimal-comma, same as
// every other amount in the app — rather than the raw Go float that would
// otherwise print as e.g. "12.5".
func TestInfoTemplate_SectionOrderAndBalanceFormatting(t *testing.T) {
	s := newInfoTestServer(t)
	var buf bytes.Buffer
	err := s.infoTmpl.Execute(&buf, infoView{
		API2PDFConfigured: true,
		Balance: render.BalanceInfo{
			Balance:    1234.5,
			HasBalance: true,
			Currency:   "USD",
			Raw:        map[string]string{"Balance": "1234.5", "Currency": "USD"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	balanceLine := ""
	for _, ln := range strings.Split(body, "\n") {
		if strings.Contains(ln, "balance-figure") {
			balanceLine = ln
		}
	}
	if want := "1\u00a0234,50 USD"; !strings.Contains(balanceLine, want) {
		t.Errorf("balance figure = %q, want it to contain %q", balanceLine, want)
	}
	// The raw float may still appear in the untouched Raw-fields table
	// below; it's the headline figure that must be formatted.
	if strings.Contains(balanceLine, "1234.5") {
		t.Errorf("balance figure = %q, still rendering as a raw Go float", balanceLine)
	}

	api := strings.Index(body, ">api2pdf<")
	toggl := strings.Index(body, ">Toggl<")
	holidays := strings.Index(body, "Holiday API (OpenHolidays)")
	if api < 0 || toggl < 0 || holidays < 0 {
		t.Fatalf("missing a section: api2pdf=%d toggl=%d holidays=%d", api, toggl, holidays)
	}
	if !(api < toggl && toggl < holidays) {
		t.Errorf("section order = api2pdf@%d toggl@%d holidays@%d, want api2pdf < toggl < holidays", api, toggl, holidays)
	}
}
