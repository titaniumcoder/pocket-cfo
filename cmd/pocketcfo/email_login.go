package main

import (
	"context"
	"log"
	"net"
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

	emailRequestsPerIPPerHour = 10
	emailRequestsPerHour      = 60
)

func (s *server) handleEmailLoginForm(w http.ResponseWriter, r *http.Request) {
	tracker.RenderEmailLogin(w, r.URL.Query().Get("error") != "")
}

func (s *server) handleEmailLoginRequest(w http.ResponseWriter, r *http.Request) {
	email := normalizeEmail(r.FormValue("email"))
	if validEmail(email) && s.allowEmailRequest(r, email) {
		if _, allowed := s.emailParts(email); allowed {
			s.sendLoginLink(email)
		}
	}

	tracker.RenderEmailSent(w)
}

func (s *server) sendLoginLink(email string) {
	token, err := auth.GenerateLoginToken(s.cfg.otpLinkSecret, email, emailLinkTTL)
	if err != nil {
		log.Printf("auth: generating login token for %s failed: %v", email, err)
		return
	}
	link := s.cfg.baseURL + "/auth/email/callback?token=" + url.QueryEscape(token)
	mailCfg := mail.Config{Region: s.cfg.sesRegion, From: s.cfg.sesFromEmail}

	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 30*time.Second)
		defer cancel()
		if err := mail.SendLoginLink(ctx, s.httpClient, mailCfg, email, link); err != nil {
			log.Printf("mail: sending login link to %s failed: %v", email, err)
		}
	}()
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
		serverError(w, r, "loading data", err)
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

func (s *server) allowEmailRequest(r *http.Request, email string) bool {
	now := time.Now()

	s.emailRequestMu.Lock()
	defer s.emailRequestMu.Unlock()

	if last, ok := s.emailRequestedAt[email]; ok && now.Sub(last) < emailRequestCooldown {
		return false
	}
	ip := clientIP(r)
	if !s.emailPerIP.allow(ip, now, emailRequestsPerIPPerHour) {
		return false
	}
	if !s.emailGlobal.allow("", now, emailRequestsPerHour) {
		return false
	}

	s.emailRequestedAt[email] = now
	return true
}

type hourlyLimiter struct {
	at map[string][]time.Time
}

func (l *hourlyLimiter) allow(key string, now time.Time, limit int) bool {
	if l.at == nil {
		l.at = map[string][]time.Time{}
	}
	cutoff := now.Add(-time.Hour)
	kept := l.at[key][:0]
	for _, t := range l.at[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		l.at[key] = kept
		return false
	}
	l.at[key] = append(kept, now)
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
