package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

func TestCurrentSession_BypassesAuthOnlyInDevelopment(t *testing.T) {
	s := &server{cfg: config{env: "development"}}
	r := httptest.NewRequest(http.MethodGet, "/", nil) // no cookie at all

	sess, ok := s.currentSession(r)
	if !ok {
		t.Fatal("want ok=true in development, got false")
	}
	if !s.authorized(sess) {
		t.Errorf("want an authorized synthetic session, got %+v", sess)
	}
}

func TestCurrentSession_UnknownEnvDoesNotBypassAuth(t *testing.T) {
	for _, env := range []string{"", "production", "Development", "anything-else"} {
		t.Run("env="+env, func(t *testing.T) {
			s := &server{cfg: config{env: env}}
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			if _, ok := s.currentSession(r); ok {
				t.Errorf("ENV=%q served a session with no cookie", env)
			}
		})
	}
}

func TestCurrentSession_RequiresRealSessionInProd(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}

	t.Run("no cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, ok := s.currentSession(r); ok {
			t.Error("want ok=false with no session cookie in prod")
		}
	})

	t.Run("valid cookie", func(t *testing.T) {
		encoded, err := auth.Encode("test-secret", auth.NewSession("octocat", "push", time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})

		sess, ok := s.currentSession(r)
		if !ok {
			t.Fatal("want ok=true with a valid session cookie")
		}
		if sess.Login != "octocat" {
			t.Errorf("Login = %q, want octocat", sess.Login)
		}
	})
}

func TestReadOnlySessionPartsAreRecheckedPerRequest(t *testing.T) {
	s := newTestServer(t)
	encoded, err := auth.Encode(s.cfg.sessionSecret,
		auth.NewReadOnlySession("person@example.com", []string{users.PartFinance, users.PartInvoicing}, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	withCookie := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
		return r
	}

	sess, ok := s.currentSession(withCookie())
	if !ok {
		t.Fatal("want the session accepted")
	}
	if sess.HasPart(users.PartFinance) {
		t.Error("a part the cookie claims but users.json does not grant survived")
	}
	if !sess.HasPart(users.PartInvoicing) {
		t.Error("a part users.json does grant was dropped")
	}

	if err := os.WriteFile(usersFile, []byte(`{"users":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.currentSession(withCookie()); ok {
		t.Error("a session for a user who is no longer listed was still accepted")
	}

	if err := os.Remove(usersFile); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.currentSession(withCookie()); !ok {
		t.Error("an unreadable users.json locked out a session it says nothing about")
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	s := newTestServer(t)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))

	for header, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want it to forbid framing", csp)
	}
}

// TestCrossSiteMutationsAreRefused: a state-changing request the browser
// marks cross-site — Sec-Fetch-Site or a foreign Origin — is refused before
// any handler runs, closing SameSite=Lax's fresh-cookie grace window.
func TestCrossSiteMutationsAreRefused(t *testing.T) {
	s := newLoginTestServer(t)

	post := func(headers map[string]string) int {
		r := httptest.NewRequest(http.MethodPost, "/refresh", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		owner, err := auth.Encode(s.cfg.sessionSecret, auth.NewSession("owner", "admin", time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: owner})
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		return w.Code
	}

	if got := post(map[string]string{"Sec-Fetch-Site": "cross-site"}); got != http.StatusForbidden {
		t.Errorf("Sec-Fetch-Site cross-site = %d, want 403", got)
	}
	if got := post(map[string]string{"Origin": "https://evil.example"}); got != http.StatusForbidden {
		t.Errorf("foreign Origin = %d, want 403", got)
	}
	if got := post(map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://example.com"}); got == http.StatusForbidden {
		t.Error("same-origin POST must pass the guard")
	}
	if got := post(map[string]string{"Sec-Fetch-Site": "same-origin"}); got == http.StatusForbidden {
		t.Error("same-origin POST without Origin must pass the guard")
	}
	// No fetch metadata at all (scripts, curl): SameSite=Lax still guards the
	// cookie, so the request proceeds to the handlers' own auth.
	if got := post(nil); got == http.StatusForbidden {
		t.Error("a request without fetch metadata must not be blanket-refused")
	}
}
