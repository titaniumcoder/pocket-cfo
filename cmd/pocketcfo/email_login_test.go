package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
)

func TestPermissionTiers(t *testing.T) {
	s := &server{}
	cases := []struct {
		permission               string
		authorized, readOnly, ok bool
	}{
		{"push", true, false, true},
		{"admin", true, false, true},
		{"readonly", false, true, true},
		{"", false, false, false},
		{"triage", false, false, false},
	}
	for _, c := range cases {
		sess := auth.Session{Permission: c.permission}
		if got := s.authorized(sess); got != c.authorized {
			t.Errorf("authorized(%q) = %v, want %v", c.permission, got, c.authorized)
		}
		if got := s.readOnly(sess); got != c.readOnly {
			t.Errorf("readOnly(%q) = %v, want %v", c.permission, got, c.readOnly)
		}
		if got := s.authenticated(sess); got != c.ok {
			t.Errorf("authenticated(%q) = %v, want %v", c.permission, got, c.ok)
		}
	}
}

func TestValidEmail(t *testing.T) {
	cases := []struct {
		email string
		want  bool
	}{
		{"person@example.com", true},
		{"", false},
		{"no-at-sign", false},
		{"@example.com", false},
		{"person@", false},
		{"has space@example.com", false},
	}
	for _, c := range cases {
		if got := validEmail(c.email); got != c.want {
			t.Errorf("validEmail(%q) = %v, want %v", c.email, got, c.want)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := normalizeEmail("  Person@Example.COM \t"); got != "person@example.com" {
		t.Errorf("normalizeEmail = %q, want person@example.com", got)
	}
}

func TestAllowEmailRequest_Cooldown(t *testing.T) {
	s := &server{emailRequestedAt: map[string]time.Time{}}

	if !s.allowEmailRequest("person@example.com") {
		t.Fatal("first request should be allowed")
	}
	if s.allowEmailRequest("person@example.com") {
		t.Fatal("immediate second request should be blocked by cooldown")
	}

	s.emailRequestedAt["person@example.com"] = time.Now().Add(-2 * emailRequestCooldown)
	if !s.allowEmailRequest("person@example.com") {
		t.Fatal("request after cooldown elapsed should be allowed")
	}
}

// newTestServer chdirs into an isolated temp dir (same convention as
// writeFixtures in client_test.go) and writes a users.json fixture there
// listing exactly "person@example.com" with invoicing access, so
// usersFile's default relative path ("data/users.json") resolves inside it.
func newTestServer(t *testing.T) *server {
	t.Helper()
	t.Chdir(t.TempDir())
	mustMkdirAll(t, filepath.Dir(usersFile))
	mustWriteFile(t, usersFile, `{"users":[{"email":"person@example.com","parts":["invoicing"]}]}`)

	return &server{
		cfg: config{
			sessionSecret: "test-session-secret",
			otpLinkSecret: "test-otp-secret",
		},
		httpClient:       &http.Client{},
		emailSentTmpl:    template.Must(template.New("t").Parse("check your email")),
		emailLoginTmpl:   template.Must(template.New("t").Parse("{{if .Error}}error{{end}}")),
		emailRequestedAt: map[string]time.Time{},
	}
}

func TestHandleEmailLoginRequest_SameResponseRegardlessOfAllowlist(t *testing.T) {
	s := newTestServer(t)

	for _, email := range []string{"person@example.com", "stranger@example.com", "not-an-email"} {
		form := url.Values{"email": {email}}
		r := httptest.NewRequest(http.MethodPost, "/auth/email", nil)
		r.PostForm = form
		w := httptest.NewRecorder()

		s.handleEmailLoginRequest(w, r)

		if w.Code != http.StatusOK {
			t.Errorf("email=%q: status = %d, want 200", email, w.Code)
		}
		if w.Body.String() != "check your email" {
			t.Errorf("email=%q: body = %q, want the generic confirmation", email, w.Body.String())
		}
	}
}

func TestHandleEmailLoginCallback_ValidTokenIssuesReadOnlySession(t *testing.T) {
	s := newTestServer(t)
	token, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, "person@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/auth/email/callback?token="+token, nil)
	w := httptest.NewRecorder()
	s.handleEmailLoginCallback(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("want redirect to /, got %d %q", w.Code, w.Header().Get("Location"))
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie {
		t.Fatalf("want a %s cookie to be set, got %+v", sessionCookie, cookies)
	}
	sess, err := auth.Decode(s.cfg.sessionSecret, cookies[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Login != "person@example.com" || !s.readOnly(sess) {
		t.Errorf("got session %+v, want readonly session for person@example.com", sess)
	}
}

func TestHandleEmailLoginCallback_RejectsExpiredOrTamperedToken(t *testing.T) {
	s := newTestServer(t)

	expired, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, "person@example.com", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"expired":   expired,
		"malformed": "not-a-token",
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/auth/email/callback?token="+token, nil)
			w := httptest.NewRecorder()
			s.handleEmailLoginCallback(w, r)

			if w.Code != http.StatusFound || w.Header().Get("Location") != "/auth/email?error=1" {
				t.Fatalf("want redirect to /auth/email?error=1, got %d %q", w.Code, w.Header().Get("Location"))
			}
			if len(w.Result().Cookies()) != 0 {
				t.Errorf("want no session cookie set, got %+v", w.Result().Cookies())
			}
		})
	}
}

func TestHandleEmailLoginCallback_RejectsNoLongerAllowlistedEmail(t *testing.T) {
	s := newTestServer(t)
	token, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, "removed@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/auth/email/callback?token="+token, nil)
	w := httptest.NewRecorder()
	s.handleEmailLoginCallback(w, r)

	if w.Header().Get("Location") != "/auth/email?error=1" {
		t.Fatalf("want redirect to /auth/email?error=1, got %q", w.Header().Get("Location"))
	}
}
