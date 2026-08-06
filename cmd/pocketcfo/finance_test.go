package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

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
