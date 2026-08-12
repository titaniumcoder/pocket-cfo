package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

// TestSafeReturnToRejectsAnotherOrigin is the security half. A login flow is
// the classic home of an open redirect, and "starts with a slash" is not the
// test people think it is.
func TestSafeReturnToRejectsAnotherOrigin(t *testing.T) {
	ok := []string{"/2026/8", "/2026/8/spending#cat-abc", "/invoicing?year=2026&month=8", "/info"}
	for _, dest := range ok {
		if !safeReturnTo(dest) {
			t.Errorf("safeReturnTo(%q) = false, want true", dest)
		}
	}
	bad := map[string]string{
		"//evil.example":          "protocol-relative: a browser resolves this to another origin",
		"///evil.example":         "the same with an extra slash",
		"/\\evil.example":         "a backslash reads as a slash in some browsers",
		"https://evil.example":    "absolute",
		"http://evil.example/x":   "absolute",
		"javascript:alert(1)":     "not a path at all",
		"/x\r\nSet-Cookie: a=b":   "header injection",
		"":                        "empty",
		"/auth/login":             "sending someone back into the login flow loops",
		"/auth/callback?code=x":   "same",
		strings.Repeat("/a", 400): "absurdly long",
	}
	for dest, why := range bad {
		if safeReturnTo(dest) {
			t.Errorf("safeReturnTo(%q) = true, want false — %s", dest, why)
		}
	}
}

// TestRefusedRequestIsRemembered: the whole point. A session expiring while
// you are reading August should not cost you August.
func TestRefusedRequestIsRemembered(t *testing.T) {
	s := newFinanceTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/2026/8?minimal=toggle", nil)
	w := httptest.NewRecorder()

	if _, ok := s.financeSession(w, r); ok {
		t.Fatal("an anonymous request was let through")
	}
	var got string
	for _, c := range w.Result().Cookies() {
		if c.Name == returnToCookie {
			got = c.Value
		}
	}
	unescaped, err := url.QueryUnescape(got)
	if err != nil {
		t.Fatal(err)
	}
	if unescaped != "/2026/8?minimal=toggle" {
		t.Errorf("remembered %q, want the address that was refused", unescaped)
	}
}

// TestLoginReturnsToTheRememberedPage closes the loop through the callback.
func TestLoginReturnsToTheRememberedPage(t *testing.T) {
	s := newFinanceTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	r.AddCookie(&http.Cookie{Name: returnToCookie, Value: url.QueryEscape("/2026/8/spending")})
	w := httptest.NewRecorder()

	if got := s.destinationAfterLogin(w, r); got != "/2026/8/spending" {
		t.Errorf("destination = %q, want the remembered page", got)
	}
	// Consumed, not read: it describes one interrupted navigation, and leaving
	// it set would redirect the next login too.
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == returnToCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the destination survives the login it was for")
	}
}

func TestLoginFallsBackToTheDashboard(t *testing.T) {
	s := newFinanceTestServer(t)
	for _, value := range []string{"", url.QueryEscape("//evil.example"), "%zz"} {
		r := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
		if value != "" {
			r.AddCookie(&http.Cookie{Name: returnToCookie, Value: value})
		}
		if got := s.destinationAfterLogin(httptest.NewRecorder(), r); got != "/" {
			t.Errorf("destination for %q = %q, want /", value, got)
		}
	}
}

// TestOnlyGETsAreRemembered: a rejected form post is not somewhere to send
// anyone back to, and replaying it after login would be worse than losing it.
func TestOnlyGETsAreRemembered(t *testing.T) {
	s := newFinanceTestServer(t)
	w := httptest.NewRecorder()
	s.rememberDestination(w, httptest.NewRequest(http.MethodPost, "/2026/8", nil))
	if len(w.Result().Cookies()) != 0 {
		t.Error("a POST was remembered as a destination")
	}
}

// TestReadOnlySessionKeepsItsDestination: the invoicing gate refuses a
// finance-only session too, and that refusal is a redirect rather than a login
// page — the cookie has to be set before it, not after.
func TestReadOnlySessionKeepsItsDestination(t *testing.T) {
	s := newFinanceTestServer(t)
	encoded, err := auth.Encode(s.cfg.sessionSecret, auth.NewReadOnlySession("someone@example.com", []string{users.PartFinance}, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/2026/8/spending", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
	w := httptest.NewRecorder()

	s.financeSpending(w, r)

	// A logged-in-but-unauthorized session is refused, not redirected to
	// login, so nothing should be remembered: logging in again changes
	// nothing for them.
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == returnToCookie {
			t.Error("a 403 was remembered as a destination to return to after login")
		}
	}
}

// TestCallbackRedirectsToTheRememberedPage runs the real handler rather than
// the helper. Testing destinationAfterLogin alone proves the lookup works and
// says nothing about whether the callback calls it — which is exactly the
// mistake worth catching, since the whole feature is one line at the end of a
// handler.
func TestCallbackRedirectsToTheRememberedPage(t *testing.T) {
	transport := oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/login/oauth/access_token"):
			return jsonResp(http.StatusOK, `{"access_token":"tok"}`), nil
		case r.URL.Path == "/user":
			return jsonResp(http.StatusOK, `{"login":"octocat"}`), nil
		case strings.Contains(r.URL.Path, "/collaborators/"):
			return jsonResp(http.StatusOK, `{"permission":"push"}`), nil
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		return nil, nil
	})
	s := &server{
		cfg:        config{clientID: "id", clientSecret: "secret", repo: "acme/data", sessionSecret: "test-secret"},
		httpClient: &http.Client{Transport: transport},
	}

	r := httptest.NewRequest(http.MethodGet, "/auth/callback?state=abc&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: stateCookie, Value: "abc"})
	r.AddCookie(&http.Cookie{Name: returnToCookie, Value: url.QueryEscape("/2026/8/spending")})
	w := httptest.NewRecorder()

	s.handleCallback(w, r)

	if got := w.Header().Get("Location"); got != "/2026/8/spending" {
		t.Errorf("redirected to %q, want the page the visitor was refused", got)
	}
}

// TestCallbackIgnoresAnotherOrigin: the same handler, handed a destination
// that would leave the site.
func TestCallbackIgnoresAnotherOrigin(t *testing.T) {
	transport := oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/login/oauth/access_token"):
			return jsonResp(http.StatusOK, `{"access_token":"tok"}`), nil
		case r.URL.Path == "/user":
			return jsonResp(http.StatusOK, `{"login":"octocat"}`), nil
		case strings.Contains(r.URL.Path, "/collaborators/"):
			return jsonResp(http.StatusOK, `{"permission":"push"}`), nil
		}
		return nil, nil
	})
	s := &server{
		cfg:        config{clientID: "id", clientSecret: "secret", repo: "acme/data", sessionSecret: "test-secret"},
		httpClient: &http.Client{Transport: transport},
	}

	r := httptest.NewRequest(http.MethodGet, "/auth/callback?state=abc&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: stateCookie, Value: "abc"})
	r.AddCookie(&http.Cookie{Name: returnToCookie, Value: url.QueryEscape("//evil.example/x")})
	w := httptest.NewRecorder()

	s.handleCallback(w, r)

	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("redirected to %q after login, want / — that is an open redirect", got)
	}
}
