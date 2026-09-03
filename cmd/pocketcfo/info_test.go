package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	toggl := strings.Index(body, ">Toggl Track<")
	toggl2 := strings.Index(body, ">Toggl 2.0<")
	holidays := strings.Index(body, "Holiday API (OpenHolidays)")
	if api < 0 || toggl < 0 || toggl2 < 0 || holidays < 0 {
		t.Fatalf("missing a section: api2pdf=%d toggl=%d toggl2=%d holidays=%d", api, toggl, toggl2, holidays)
	}
	if !(api < toggl && toggl < toggl2 && toggl2 < holidays) {
		t.Errorf("section order = api2pdf@%d toggl@%d toggl2@%d holidays@%d, want api2pdf < toggl < toggl2 < holidays", api, toggl, toggl2, holidays)
	}
}

func TestHandleInfo_ShowsTheToggl2Panel(t *testing.T) {
	s := newInfoTestServer(t)
	yesterday := time.Now().AddDate(0, 0, -1)
	s.togglFocus = tracker.NewFocus(tracker.FocusConfig{Key: "toggl_sk_x", OrganizationID: "10", WorkspaceID: "20", KeyExpiresAt: yesterday}, focusTransport(t, 20, 10, http.StatusOK))
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

// focusTransport answers the three Toggl 2.0 calls /info makes: the clients
// list, the account settings, and the workspace context (with contextStatus).
func focusTransport(t *testing.T, workspace, organization, contextStatus int) *http.Client {
	t.Helper()
	return &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "focus.toggl.com" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/clients"):
			return jsonResp(http.StatusOK, `{"data":[{"id":5,"name":"Acme"}],"page":1,"per_page":200}`), nil
		case strings.HasSuffix(r.URL.Path, "/users/me/settings"):
			return jsonResp(http.StatusOK, `{"current_workspace_id":`+strconv.Itoa(workspace)+`}`), nil
		case strings.HasSuffix(r.URL.Path, "/context"):
			if contextStatus != http.StatusOK {
				return jsonResp(contextStatus, `{"error":"forbidden","error_description":"session authentication only"}`), nil
			}
			return jsonResp(http.StatusOK, `{"workspace_id":`+strconv.Itoa(workspace)+`,"organization_id":`+strconv.Itoa(organization)+`}`), nil
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		return nil, nil
	})}
}

func TestHandleInfo_KeyOnlyToggl2ShowsTheIdsItCanSee(t *testing.T) {
	s := newInfoTestServer(t)
	s.togglFocus = tracker.NewFocus(tracker.FocusConfig{Key: "toggl_sk_x"}, focusTransport(t, 20, 10, http.StatusForbidden))
	s.cfg.finance.Toggl2Key = "toggl_sk_x"

	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"Only <code>TOGGL2_API_KEY</code> is set",
		`<td class="code">20</td>`,
		"not answered for an API key",
		"/organizations/&lt;id&gt;/workspaces/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "Feeds the dashboard") || strings.Contains(body, "Not configured (TOGGL2_API_KEY") {
		t.Error("a key-only panel is neither active nor unconfigured")
	}
	if strings.Contains(body, "status 403") {
		t.Error("the documented refusal is shown as a raw error")
	}
}

func TestHandleInfo_ConfiguredToggl2ChecksTheIdsAgainstTheKey(t *testing.T) {
	s := newInfoTestServer(t)
	s.togglFocus = tracker.NewFocus(tracker.FocusConfig{Key: "toggl_sk_x", OrganizationID: "10", WorkspaceID: "21"}, focusTransport(t, 20, 10, http.StatusOK))
	s.cfg.togglMode = togglModeFocus
	s.cfg.finance.Toggl2Key = "toggl_sk_x"
	s.cfg.finance.Toggl2Organization = "10"
	s.cfg.finance.Toggl2Workspace = "21"
	s.tracker.Toggl = s.togglFocus

	w := httptest.NewRecorder()
	s.handleInfo(w, authorizedRequest(t, s))
	body := w.Body.String()
	if !strings.Contains(body, "currently works in workspace 20, not the configured 21") {
		t.Errorf("no mismatch note on the page:\n%s", body)
	}
	if !strings.Contains(body, `<td class="code">10</td>`) {
		t.Error("the organization id the context returned is missing")
	}
}
