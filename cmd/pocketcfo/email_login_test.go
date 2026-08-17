package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
)

func TestPermissionTiers(t *testing.T) {
	s := &server{}
	tests := []struct {
		name                     string
		permission               string
		authorized, readOnly, ok bool
	}{
		{"push", "push", true, false, true},
		{"admin", "admin", true, false, true},
		{"readonly", "readonly", false, true, true},
		{"empty", "", false, false, false},
		{"triage", "triage", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := auth.Session{Permission: tt.permission}
			if got := s.authorized(sess); got != tt.authorized {
				t.Errorf("authorized(%q) = %v, want %v", tt.permission, got, tt.authorized)
			}
			if got := s.readOnly(sess); got != tt.readOnly {
				t.Errorf("readOnly(%q) = %v, want %v", tt.permission, got, tt.readOnly)
			}
			if got := s.authenticated(sess); got != tt.ok {
				t.Errorf("authenticated(%q) = %v, want %v", tt.permission, got, tt.ok)
			}
		})
	}
}

func TestValidEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  bool
	}{
		{"valid", "person@example.com", true},
		{"empty", "", false},
		{"no at sign", "no-at-sign", false},
		{"missing local part", "@example.com", false},
		{"missing domain", "person@", false},
		{"space in local part", "has space@example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validEmail(tt.email); got != tt.want {
				t.Errorf("validEmail(%q) = %v, want %v", tt.email, got, tt.want)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := normalizeEmail("  Person@Example.COM \t"); got != "person@example.com" {
		t.Errorf("normalizeEmail = %q, want person@example.com", got)
	}
}

func emailPost(remoteAddr string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/auth/email", nil)
	r.RemoteAddr = remoteAddr
	return r
}

func TestAllowEmailRequestCooldown(t *testing.T) {
	s := &server{emailRequestedAt: map[string]time.Time{}}
	r := emailPost("192.0.2.1:1234")

	if !s.allowEmailRequest(r, "person@example.com") {
		t.Fatal("first request should be allowed")
	}
	if s.allowEmailRequest(r, "person@example.com") {
		t.Fatal("immediate second request should be blocked by cooldown")
	}

	s.emailRequestedAt["person@example.com"] = time.Now().Add(-2 * emailRequestCooldown)
	if !s.allowEmailRequest(r, "person@example.com") {
		t.Fatal("request after cooldown elapsed should be allowed")
	}
}

func TestAllowEmailRequestPerIPAndGlobalCeilings(t *testing.T) {
	t.Run("one host is capped however many addresses it tries", func(t *testing.T) {
		s := &server{emailRequestedAt: map[string]time.Time{}}
		r := emailPost("192.0.2.7:1234")

		allowed := 0
		for i := range emailRequestsPerIPPerHour * 3 {
			if s.allowEmailRequest(r, fmt.Sprintf("person%d@example.com", i)) {
				allowed++
			}
		}
		if allowed != emailRequestsPerIPPerHour {
			t.Errorf("%d requests allowed from one host, want %d", allowed, emailRequestsPerIPPerHour)
		}

		if !s.allowEmailRequest(emailPost("198.51.100.9:1234"), "someone@example.com") {
			t.Error("a second host was blocked by the first host's ceiling")
		}
	})

	t.Run("the whole endpoint has a ceiling too", func(t *testing.T) {
		s := &server{emailRequestedAt: map[string]time.Time{}}
		allowed := 0
		for i := range emailRequestsPerHour * 2 {
			r := emailPost(fmt.Sprintf("192.0.2.%d:1234", i%250+1))
			if s.allowEmailRequest(r, fmt.Sprintf("p%d@example.com", i)) {
				allowed++
			}
		}
		if allowed != emailRequestsPerHour {
			t.Errorf("%d requests allowed overall, want %d", allowed, emailRequestsPerHour)
		}
	})
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
		emailRequestedAt: map[string]time.Time{},
	}
}

// TestEmailParts_AllowsAnyListedPartNotJustInvoicing is a regression test:
// emailParts used to additionally require users.HasPart(..., PartInvoicing),
// which meant a user listed with only "finance" access could never
// request a login link at all (this binary serves both finance and
// invoicing — see financeSession/checkInvoicingAccess — so that extra
// requirement was simply wrong).
func TestEmailParts_AllowsAnyListedPartNotJustInvoicing(t *testing.T) {
	s := &server{}
	t.Chdir(t.TempDir())
	mustMkdirAll(t, filepath.Dir(usersFile))
	mustWriteFile(t, usersFile, `{"users":[
		{"email":"finance-only@example.com","parts":["finance"]},
		{"email":"invoicing-only@example.com","parts":["invoicing"]},
		{"email":"both@example.com","parts":["finance","invoicing"]}
	]}`)

	tests := []struct {
		email     string
		wantOK    bool
		wantParts []string
	}{
		{"finance-only@example.com", true, []string{"finance"}},
		{"invoicing-only@example.com", true, []string{"invoicing"}},
		{"both@example.com", true, []string{"finance", "invoicing"}},
		{"unlisted@example.com", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			parts, ok := s.emailParts(tt.email)
			if ok != tt.wantOK {
				t.Errorf("emailParts(%q) ok = %v, want %v", tt.email, ok, tt.wantOK)
			}
			if ok && (len(parts) != len(tt.wantParts) || parts[0] != tt.wantParts[0]) {
				t.Errorf("emailParts(%q) parts = %v, want %v", tt.email, parts, tt.wantParts)
			}
		})
	}
}

// TestHandleEmailLoginCallback_FinanceOnlyUserCanLogIn is the end-to-end
// regression test for the same bug: a finance-only user must be able to
// complete the full login-link flow, not just pass the emailParts check in
// isolation.
func TestHandleEmailLoginCallback_FinanceOnlyUserCanLogIn(t *testing.T) {
	s := newTestServer(t)
	mustWriteFile(t, usersFile, `{"users":[{"email":"finance-only@example.com","parts":["finance"]}]}`)

	token, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, "finance-only@example.com", time.Hour)
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
	if !sess.HasPart("finance") {
		t.Errorf("session parts = %v, want finance", sess.Parts)
	}
}

func TestHandleEmailLoginRequestSameResponseRegardlessOfAllowlist(t *testing.T) {
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
		if !strings.Contains(w.Body.String(), "a login link is on its way") {
			t.Errorf("email=%q: body = %q, want the generic confirmation", email, w.Body.String())
		}
	}
}

func TestHandleEmailLoginCallbackValidTokenIssuesReadOnlySession(t *testing.T) {
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

func TestHandleEmailLoginCallbackRejectsExpiredOrTamperedToken(t *testing.T) {
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

func TestHandleEmailLoginCallbackRejectsNoLongerAllowlistedEmail(t *testing.T) {
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
