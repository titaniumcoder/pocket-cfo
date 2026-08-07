package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

// newFinanceTestServer builds a server with a real (network-free) Tracker:
// recipients/invoices dirs exist but are empty. The HTTP client's transport
// answers the holidays API with an empty (but valid) result instead of
// reaching the real openholidaysapi.org - Toggl stays nil (no credentials
// configured), so it never makes a request at all; anything unexpected
// fails the test outright rather than silently hitting the network.
func newFinanceTestServer(t *testing.T) *server {
	t.Helper()
	t.Chdir(t.TempDir())
	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)

	noNetwork := &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "openholidaysapi.org") {
			return jsonResp(http.StatusOK, `[]`), nil
		}
		t.Fatalf("unexpected request in a network-free finance test: %s %s", r.Method, r.URL)
		return nil, nil
	})}

	return &server{
		cfg:        config{env: "prod", sessionSecret: "test-secret"},
		httpClient: noNetwork,
		tracker:    buildTracker(financeconfig.Config{}, noNetwork, "data"),
	}
}

func readOnlyRequest(t *testing.T, s *server, path string, parts []string) *http.Request {
	t.Helper()
	encoded, err := auth.Encode(s.cfg.sessionSecret, auth.NewReadOnlySession("someone@example.com", parts, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
	return r
}

func TestFinanceSession_InvoicingOnlyRedirectsToInvoicing(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := readOnlyRequest(t, s, "/", []string{users.PartInvoicing})
	w := httptest.NewRecorder()

	_, ok := s.financeSession(w, r)
	if ok {
		t.Fatal("want ok=false for an invoicing-only session on a finance route")
	}
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/invoicing" {
		t.Errorf("want redirect to /invoicing, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestFinanceSession_FinanceOnlyPasses(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance})
	w := httptest.NewRecorder()

	sess, ok := s.financeSession(w, r)
	if !ok {
		t.Fatalf("want ok=true for a finance-scoped session, got response %d", w.Code)
	}
	if sess.Login != "someone@example.com" {
		t.Errorf("Login = %q, want someone@example.com", sess.Login)
	}
}

func TestFinanceSession_NoPartsIsForbidden(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := readOnlyRequest(t, s, "/", nil)
	w := httptest.NewRecorder()

	if _, ok := s.financeSession(w, r); ok {
		t.Fatal("want ok=false for a session with no parts at all")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestFinanceSession_UnauthenticatedShowsLogin(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := httptest.NewRequest(http.MethodGet, "/", nil) // no cookie
	w := httptest.NewRecorder()

	if _, ok := s.financeSession(w, r); ok {
		t.Fatal("want ok=false with no session at all")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (the login page itself)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Finance Tracker") {
		t.Error("want the login page body, got something else")
	}
}

func TestHandleIndex_FinanceOnlyRedirectsToRoot(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	r := readOnlyRequest(t, s, "/invoicing", []string{users.PartFinance})
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("want redirect to /, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestHandleIndex_NoPartsIsForbidden(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	r := readOnlyRequest(t, s, "/invoicing", nil)
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestFinanceCurrentMonth_RendersForAuthorizedSession(t *testing.T) {
	s := newFinanceTestServer(t)
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance})
	w := httptest.NewRecorder()

	s.financeCurrentMonth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Finance Tracker") {
		t.Error("want the finance dashboard body")
	}
}

func TestFinanceYear_InvalidYearRedirects(t *testing.T) {
	s := newFinanceTestServer(t)
	r := readOnlyRequest(t, s, "/1900", []string{users.PartFinance})
	r.SetPathValue("year", "1900")
	w := httptest.NewRecorder()

	s.financeYear(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("want redirect to / for an out-of-range year, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestFinanceMonth_ValidYearMonthRenders(t *testing.T) {
	s := newFinanceTestServer(t)
	now := time.Now()
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance})
	r.SetPathValue("year", strconv.Itoa(now.Year()))
	r.SetPathValue("month", strconv.Itoa(int(now.Month())))
	w := httptest.NewRecorder()

	s.financeMonth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
}

func TestFinanceAPIAuth_RejectsWrongPassword(t *testing.T) {
	s := newFinanceTestServer(t)
	s.cfg.finance.APIPassword = "correct-password"
	called := false
	handler := s.financeAPIAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	r := httptest.NewRequest(http.MethodGet, "/api/net-income/2026", nil)
	r.Header.Set("X-API-Password", "wrong")
	w := httptest.NewRecorder()
	handler(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if called {
		t.Error("next handler must not run on a bad password")
	}
}

func TestFinanceAPIAuth_AcceptsBearerToken(t *testing.T) {
	s := newFinanceTestServer(t)
	s.cfg.finance.APIPassword = "correct-password"
	called := false
	handler := s.financeAPIAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	r := httptest.NewRequest(http.MethodGet, "/api/net-income/2026", nil)
	r.Header.Set("Authorization", "Bearer correct-password")
	w := httptest.NewRecorder()
	handler(w, r)

	if !called {
		t.Error("want the next handler to run with a valid bearer token")
	}
}

func TestFinanceAPINetIncomeYear_ReturnsJSON(t *testing.T) {
	s := newFinanceTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/net-income/2026", nil)
	r.SetPathValue("year", "2026")
	w := httptest.NewRecorder()

	s.financeAPINetIncomeYear(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (body: %s)", err, w.Body.String())
	}
	if resp["mode"] != "year" {
		t.Errorf("mode = %v, want year", resp["mode"])
	}
}

// TestWriteNetIncome_ErrorPathReturnsJSON is a direct regression test for
// writeJSONError: writeNetIncome's error path must stay JSON like its
// success path, not fall through to http.Error's plain-text body.
func TestWriteNetIncome_ErrorPathReturnsJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeNetIncome(w, tracker.Figures{Personal: tracker.PersonalView{Err: "company income unavailable"}})

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not valid JSON: %v (body: %s)", err, w.Body.String())
	}
	if resp.Error != "company income unavailable" {
		t.Errorf("error = %q, want %q", resp.Error, "company income unavailable")
	}
}

func TestFinanceAPINetIncomeMonth_InvalidMonthReturnsBadRequest(t *testing.T) {
	s := newFinanceTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/net-income/2026/13", nil)
	r.SetPathValue("year", "2026")
	r.SetPathValue("month", "13")
	w := httptest.NewRecorder()

	s.financeAPINetIncomeMonth(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestWriteJSONError(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONError(w, http.StatusBadGateway, "boom")

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if resp.Error != "boom" {
		t.Errorf("error = %q, want boom", resp.Error)
	}
}

func TestIsRefreshAndIsMinimalToggle(t *testing.T) {
	tests := []struct {
		name              string
		query             string
		wantRefresh       bool
		wantMinimalToggle bool
	}{
		{"neither", "", false, false},
		{"refresh", "?refresh=1", true, false},
		{"minimal toggle", "?minimal=toggle", false, true},
		{"minimal wrong value", "?minimal=other", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/"+tt.query, nil)
			if got := isRefresh(r); got != tt.wantRefresh {
				t.Errorf("isRefresh = %v, want %v", got, tt.wantRefresh)
			}
			if got := isMinimalToggle(r); got != tt.wantMinimalToggle {
				t.Errorf("isMinimalToggle = %v, want %v", got, tt.wantMinimalToggle)
			}
		})
	}
}

func TestHandleInvoicePDF_ServesExistingFile(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing/invoices/INV-0000000001.pdf", []string{users.PartInvoicing})
	r.SetPathValue("file", "INV-0000000001.pdf")
	w := httptest.NewRecorder()

	s.handleInvoicePDF(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "pdf-1" {
		t.Errorf("body = %q, want pdf-1", w.Body.String())
	}
}

func TestHandleInvoicePDF_RejectsPathTraversal(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing/invoices/traversal.pdf", []string{users.PartInvoicing})
	r.SetPathValue("file", "../secret.pdf")
	w := httptest.NewRecorder()

	s.handleInvoicePDF(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a path-traversal attempt", w.Code)
	}
}

func TestHandleInvoicePDF_UnauthenticatedRedirectsToLogin(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	r := httptest.NewRequest(http.MethodGet, "/invoicing/invoices/INV-0000000001.pdf", nil)
	r.SetPathValue("file", "INV-0000000001.pdf")
	w := httptest.NewRecorder()

	s.handleInvoicePDF(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/auth/login" {
		t.Errorf("want redirect to /auth/login, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestHandleIndex_AuthorizedRendersInvoicingDashboard(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing", []string{users.PartInvoicing})
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	// Unlike the client portal (scoped by a recipient's own token), the
	// admin invoicing dashboard lists every recipient's invoices, including
	// drafts (see stats.LoadInvoices).
	body := w.Body.String()
	for _, number := range []string{"INV-0000000001", "INV-0000000002", "INV-0000000003"} {
		if !strings.Contains(body, number) {
			t.Errorf("%s missing from the invoicing dashboard", number)
		}
	}
}
