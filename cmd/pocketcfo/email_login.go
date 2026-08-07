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
	// emailLinkTTL is how long an unclicked email login link stays valid —
	// see ARCHITECTURE.md §8. Once clicked, the resulting session instead
	// runs for auth.ReadOnlyTTL.
	emailLinkTTL = 15 * time.Minute

	// emailRequestCooldown is a soft, in-memory-only throttle (see
	// server.emailRequestedAt) against repeatedly re-triggering an email to
	// the same address.
	emailRequestCooldown = 60 * time.Second
)

// handleEmailLoginForm serves the email-entry form at GET /auth/email.
func (s *server) handleEmailLoginForm(w http.ResponseWriter, r *http.Request) {
	tracker.RenderEmailLogin(w, r.URL.Query().Get("error") != "")
}

// handleEmailLoginRequest handles POST /auth/email. It always renders the
// same "check your email" confirmation regardless of whether the address is
// allowlisted, malformed, or rate-limited — there is no observable
// difference, so submitting the form can't be used to probe which
// addresses are authorized.
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

// handleEmailLoginCallback handles GET /auth/email/callback?token=... —
// verifying the signed, self-expiring token and, on success, issuing a
// readonly session cookie. See auth.VerifyLoginToken and
// ARCHITECTURE.md §8's design note on why this is a self-expiring bearer
// link rather than a server-tracked single-use code.
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
	http.Redirect(w, r, "/", http.StatusFound)
}

// emailLoginAvailable reports whether the email-login option is worth
// offering at all: it can only ever succeed for an address listed in
// users.json, so with an empty (or unreadable) list the link would lead
// straight to a dead end. Read fresh, like every other users.json check, so
// adding the first user makes the option appear without a restart.
func (s *server) emailLoginAvailable() bool {
	u, err := users.Load(usersFile)
	if err != nil {
		return false
	}
	return len(u.Users) > 0
}

// emailParts reports the part(s) email is allowed on this deployment's
// users.json (see internal/users), and whether it's listed at all — for
// *any* part, not just invoicing: this binary serves both the finance
// tracker and invoicing (see financeSession/checkInvoicingAccess), so a
// user listed with only "finance" must still be able to request a login
// link. Which page(s) they can actually reach is enforced downstream by
// each route's own sess.HasPart check, not here. A read failure
// (missing/malformed users.json) fails closed, denying access, same as an
// unlisted email.
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

// validEmail is a minimal sanity check, not a full RFC 5322 validator —
// good enough to reject empty/garbage input before it touches the
// allowlist check or triggers an email send.
func validEmail(email string) bool {
	if email == "" || len(email) > 254 || strings.ContainsAny(email, " \t\n") {
		return false
	}
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1
}
