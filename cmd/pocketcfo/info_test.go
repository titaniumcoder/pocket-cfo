package main

import (
	"html/template"
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
	infoTmpl := template.Must(template.New("info.html").Funcs(templateFuncs).ParseFiles(filepath.Join(wd, "..", "..", "templates", "info.html")))

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

func TestHandleInfo_ForbiddenForUnauthenticated(t *testing.T) {
	s := newInfoTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/info", nil) // no cookie
	w := httptest.NewRecorder()

	s.handleInfo(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
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
