package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
)

func TestRandomState(t *testing.T) {
	a, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("want two different random states, got the same value twice")
	}
	if _, err := base64.URLEncoding.DecodeString(a); err != nil {
		t.Errorf("randomState() = %q, not valid base64: %v", a, err)
	}
}

func TestHandleLogin(t *testing.T) {
	s := &server{cfg: config{clientID: "client-id", baseURL: "https://example.com"}}
	r := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	s.handleLogin(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusFound)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "https://github.com/login/oauth/authorize") {
		t.Errorf("Location = %q, want the GitHub authorize URL", loc)
	}
	if !strings.Contains(loc, "client_id=client-id") {
		t.Errorf("Location = %q, missing client_id", loc)
	}

	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != stateCookie {
		t.Fatalf("want a %s cookie set, got %+v", stateCookie, cookies)
	}
	if !strings.Contains(loc, "state="+cookies[0].Value) {
		t.Error("state cookie value must match the state param in the redirect URL")
	}
}

func TestHandleLogout(t *testing.T) {
	s := &server{cfg: config{}}
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	w := httptest.NewRecorder()

	s.handleLogout(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("want redirect to /, got %d %q", w.Code, w.Header().Get("Location"))
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].MaxAge >= 0 {
		t.Fatalf("want session cookie cleared, got %+v", cookies)
	}
}

func TestHandleCallback_InvalidState(t *testing.T) {
	s := &server{}
	tests := []struct {
		name        string
		stateCookie string
		queryState  string
	}{
		{"no cookie at all", "", ""},
		{"cookie present, query missing", "abc", ""},
		{"cookie and query mismatch", "abc", "def"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+tt.queryState, nil)
			if tt.stateCookie != "" {
				r.AddCookie(&http.Cookie{Name: stateCookie, Value: tt.stateCookie})
			}
			w := httptest.NewRecorder()
			s.handleCallback(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
}

func TestHandleCallback_MissingCode(t *testing.T) {
	s := &server{}
	r := httptest.NewRequest(http.MethodGet, "/auth/callback?state=abc", nil)
	r.AddCookie(&http.Cookie{Name: stateCookie, Value: "abc"})
	w := httptest.NewRecorder()
	s.handleCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// oauthRoundTripFunc adapts a func to http.RoundTripper - the same idiomatic
// per-package fake-transport pattern used elsewhere in this repo (see
// internal/auth/github_test.go, internal/render/api2pdf_test.go); kept
// package-local rather than shared, same as those.
type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestHandleCallback_HappyPathIssuesSessionForCollaborator(t *testing.T) {
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
	w := httptest.NewRecorder()
	s.handleCallback(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("want redirect to /, got %d %q", w.Code, w.Header().Get("Location"))
	}
	var sessionCookieVal string
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			sessionCookieVal = c.Value
		}
	}
	if sessionCookieVal == "" {
		t.Fatal("want a session cookie set")
	}
	sess, err := auth.Decode("test-secret", sessionCookieVal)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Login != "octocat" || sess.Permission != "push" {
		t.Errorf("session = %+v, want Login=octocat Permission=push", sess)
	}
}

func TestHandleCallback_ForbiddenWhenNotACollaborator(t *testing.T) {
	transport := oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/login/oauth/access_token"):
			return jsonResp(http.StatusOK, `{"access_token":"tok"}`), nil
		case r.URL.Path == "/user":
			return jsonResp(http.StatusOK, `{"login":"stranger"}`), nil
		case strings.Contains(r.URL.Path, "/collaborators/"):
			return jsonResp(http.StatusNotFound, ``), nil
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
	w := httptest.NewRecorder()
	s.handleCallback(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	// The state cookie is always cleared once consumed, regardless of
	// outcome - only a *session* cookie must be absent for a forbidden login.
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Errorf("want no session cookie set, got %+v", c)
		}
	}
}
