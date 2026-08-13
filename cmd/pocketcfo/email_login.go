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
	emailLinkTTL = 15 * time.Minute

	emailRequestCooldown = 60 * time.Second
)

func (s *server) handleEmailLoginForm(w http.ResponseWriter, r *http.Request) {
	tracker.RenderEmailLogin(w, r.URL.Query().Get("error") != "")
}

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

func (s *server) handleEmailLoginCallback(w http.ResponseWriter, r *http.Request) {
	email, err := auth.VerifyLoginToken(s.cfg.otpLinkSecret, r.URL.Query().Get("token"))
	if err != nil {
		http.Redirect(w, r, "/auth/email?error=1", http.StatusFound)
		return
	}
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

func (s *server) emailLoginAvailable() bool {
	u, err := users.Load(usersFile)
	if err != nil {
		return false
	}
	return len(u.Users) > 0
}

func (s *server) emailParts(email string) ([]string, bool) {
	u, err := users.Load(usersFile)
	if err != nil {
		log.Printf("users: loading %s failed: %v", usersFile, err)
		return nil, false
	}
	return users.PartsFor(u, email)
}

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

func validEmail(email string) bool {
	if email == "" || len(email) > 254 || strings.ContainsAny(email, " \t\n") {
		return false
	}
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1
}
