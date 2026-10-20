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
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
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

// newClientPortalServer is newTestServer with a recipient whose own email
// address can log in: recipient 0001 with a passkey, and the link secret
// that turns passkey into portal URL.
func newClientPortalServer(t *testing.T, usersJSON string) *server {
	t.Helper()
	s := newTestServer(t)
	if usersJSON != "" {
		mustWriteFile(t, usersFile, usersJSON)
	}
	mustMkdirAll(t, recipientsDir)
	writeJSON(t, recipientsDir+"/0001.json", recipient.RecipientJson{
		Number: 1, LegalName: "Portal Client", Email: "client@example.com", IsBusiness: true,
		Language:      recipient.RecipientJsonLanguageDe,
		Address:       recipient.Address{Line1: "Weg 1", PostalCode: "1000", City: "Wien", CountryCode: "AT"},
		AccessPasskey: strp("portal-passkey"),
	})
	s.cfg.clientLinkSecret = "portal-link-secret"
	return s
}

// TestAClientLoginLandsOnItsPortal: a recipient logging in with the address
// the recipient data carries is sent straight to the portal their passkey
// link opens — no users.json entry needed.
func TestAClientLoginLandsOnItsPortal(t *testing.T) {
	s := newClientPortalServer(t, "")

	token, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, "client@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/email/callback?token="+token, nil)
	w := httptest.NewRecorder()
	s.handleEmailLoginCallback(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/invoicing/client" {
		t.Fatalf("want redirect to the portal, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

// TestAClientWithPartsLandsOnItsPortalAndKeepsItsParts: a client who also
// carries parts gets the portal as the landing page, and the session keeps
// the parts — the access is not narrowed by the portal redirect.
func TestAClientWithPartsLandsOnItsPortalAndKeepsItsParts(t *testing.T) {
	s := newClientPortalServer(t, `{"users":[{"email":"client@example.com","parts":["finance"]}]}`)

	token, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, "client@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/email/callback?token="+token, nil)
	w := httptest.NewRecorder()
	s.handleEmailLoginCallback(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/invoicing/client" {
		t.Fatalf("want redirect to the portal, got %d %q", w.Code, w.Header().Get("Location"))
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
		t.Errorf("session parts = %v, want finance kept beside the portal", sess.Parts)
	}
}

// TestAClientLoginRequestIsAnsweredLikeAnyOther: a recipient address may
// request a login link, and the response is the same generic page whether
// the address is known or not.
func TestAClientLoginRequestIsAnsweredLikeAnyOther(t *testing.T) {
	s := newClientPortalServer(t, "")

	for _, email := range []string{"client@example.com", "stranger@example.com"} {
		form := url.Values{"email": {email}}
		r := httptest.NewRequest(http.MethodPost, "/auth/email", nil)
		r.PostForm = form
		w := httptest.NewRecorder()
		s.handleEmailLoginRequest(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("login request for %s = %d, want the generic 200", email, w.Code)
		}
	}
}

// TestAPureClientSessionOpensItsPortal: after the email login, the session
// itself opens the portal — no passkey, no token in any URL — while the
// finance and invoicing pages stay closed to a parts-less client.
func TestAPureClientSessionOpensItsPortal(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.sessionSecret = "test-session-secret"
	t.Chdir(t.TempDir())
	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)
	writeJSON(t, recipientsDir+"/0001.json", recipient.RecipientJson{
		Number: 1, LegalName: "Portal Client", Email: "client@example.com", IsBusiness: true,
		Language: recipient.RecipientJsonLanguageDe,
		Address:  recipient.Address{Line1: "Weg 1", PostalCode: "1000", City: "Wien", CountryCode: "AT"},
	})

	encoded, err := auth.Encode(s.cfg.sessionSecret, auth.NewReadOnlySession("client@example.com", nil, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sessionRequest := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, r)
		return w
	}

	portal := sessionRequest("/invoicing/client")
	if portal.Code != http.StatusOK {
		t.Fatalf("portal via session = %d, want 200:\n%s", portal.Code, portal.Body.String())
	}
	if !strings.Contains(portal.Body.String(), "Portal Client") {
		t.Errorf("the session portal shows the wrong client:\n%s", portal.Body.String())
	}
	if strings.Contains(portal.Body.String(), "/invoicing/client//invoices/") {
		t.Error("the session portal must not build PDF links from an empty token")
	}

	finance := sessionRequest("/2026/10")
	if finance.Code == http.StatusOK {
		t.Errorf("a parts-less client session reached the finance page (%d), want it refused", finance.Code)
	}
}

// TestAPasskeyLinkKeepsWorkingBesideTheSession: the owner-shared link still
// opens the portal without any session, and garbage tokens stay a 404.
func TestAPasskeyLinkKeepsWorkingBesideTheSession(t *testing.T) {
	s := newTestClientServer(t)
	t.Chdir(t.TempDir())
	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)
	writeJSON(t, recipientsDir+"/0001.json", recipient.RecipientJson{
		Number: 1, LegalName: "Portal Client", Email: "client@example.com", IsBusiness: true,
		Language:      recipient.RecipientJsonLanguageDe,
		Address:       recipient.Address{Line1: "Weg 1", PostalCode: "1000", City: "Wien", CountryCode: "AT"},
		AccessPasskey: strp("portal-passkey"),
	})

	good := "/invoicing/client/" + computeToken(1, "portal-passkey", "secret")
	if w := s.serveGet(good); w.Code != http.StatusOK {
		t.Errorf("passkey link = %d, want 200", w.Code)
	}
	if w := s.serveGet("/invoicing/client/garbage"); w.Code != http.StatusNotFound {
		t.Errorf("garbage token without a session = %d, want 404", w.Code)
	}
}

func (s *server) serveGet(path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}
