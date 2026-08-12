package main

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/mail"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

const (
	// How long an unclicked link stays valid; the session it creates then runs
	// for auth.ReadOnlyTTL instead.
	emailLinkTTL = 15 * time.Minute

	// Soft, in-memory throttle against re-triggering mail to one address.
	emailRequestCooldown = 60 * time.Second
)

// handleEmailLoginForm serves the email-entry form at GET /auth/email.
func (s *server) handleEmailLoginForm(w http.ResponseWriter, r *http.Request) {
	tracker.RenderEmailLogin(w, r.URL.Query().Get("error") != "")
}

// handleEmailLoginRequest always renders the same confirmation whether the
// address is allowlisted, malformed or rate-limited, so the form can't be used
// to probe which addresses are authorized.
func (s *server) handleEmailLoginRequest(w http.ResponseWriter, r *http.Request) {
	email := normalizeEmail(r.FormValue("email"))
	_, allowed := s.emailParts(email)
	if validEmail(email) && allowed && s.allowEmailRequest(email) {
		s.sendLoginLink(r, email)
	}

	tracker.RenderEmailSent(w)
}

func (s *server) sendLoginLink(r *http.Request, email string) {
	token, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, email, emailLinkTTL)
	if err != nil {
		log.Printf("auth: generating login token for %s failed: %v", email, err)
		return
	}
	link := s.cfg.baseURL + "/auth/email/callback?token=" + url.QueryEscape(token)

	mailCfg := mail.Config{Region: s.cfg.sesRegion, From: s.cfg.sesFromEmail}
	if err := mail.SendLoginLink(r.Context(), s.httpClient, mailCfg, email, link); err != nil {
		log.Printf("mail: sending login link to %s failed: %v", email, err)
	}
}

// handleEmailLoginCallback verifies the signed, self-expiring token and issues
// a readonly session. ARCHITECTURE.md §8 covers why it's a bearer link rather
// than a server-tracked single-use code.
func (s *server) handleEmailLoginCallback(w http.ResponseWriter, r *http.Request) {
	email, err := auth.VerifyLoginToken(s.cfg.otpLinkSecret, r.URL.Query().Get("token"))
	if err != nil {
		http.Redirect(w, r, "/auth/email?error=1", http.StatusFound)
		return
	}
	// Re-check users.json at click time too, not just at request time — it
	// may have changed since the link was issued.
	parts, allowed := s.emailParts(email)
	if !allowed {
		http.Redirect(w, r, "/auth/email?error=1", http.StatusFound)
		return
	}

	sess := auth.NewReadOnlySession(email, parts, auth.ReadOnlyTTL)
	encoded, err := auth.Encode(s.cfg.sessionSecret, sess)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(auth.ReadOnlyTTL),
	})
	http.Redirect(w, r, s.destinationAfterLogin(w, r), http.StatusFound)
}

// emailLoginAvailable hides the option when users.json is empty or unreadable,
// since the link would lead to a dead end. Read fresh, so adding the first user
// makes it appear without a restart.
func (s *server) emailLoginAvailable() bool {
	u, err := users.Load(usersFile)
	if err != nil {
		return false
	}
	return len(u.Users) > 0
}

// emailParts reports which parts email may reach, and whether it is listed for
// ANY of them — a user scoped to finance alone must still be able to request a
// link. Which pages they actually reach is each route's own sess.HasPart check.
// A read failure fails closed, same as an unlisted address.
func (s *server) emailParts(email string) ([]string, bool) {
	u, err := users.Load(usersFile)
	if err != nil {
		log.Printf("users: loading %s failed: %v", usersFile, err)
		return nil, false
	}
	return users.PartsFor(u, email)
}

// allowEmailRequest applies emailRequestCooldown per address so repeatedly
// submitting the same address doesn't spam their inbox.
func (s *server) allowEmailRequest(email string) bool {
	s.emailRequestMu.Lock()
	defer s.emailRequestMu.Unlock()

	if last, ok := s.emailRequestedAt[email]; ok && time.Since(last) < emailRequestCooldown {
		return false
	}
	s.emailRequestedAt[email] = time.Now()
	return true
}

func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// validEmail is a sanity check, not an RFC 5322 validator: enough to reject
// garbage before it reaches the allowlist or triggers a send.
func validEmail(email string) bool {
	if email == "" || len(email) > 254 || strings.ContainsAny(email, " \t\n") {
		return false
	}
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1
}
