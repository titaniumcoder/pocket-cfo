package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
)

const (
	stateCookie = "pocketcfo_oauth_state"
	stateTTL    = 10 * time.Minute
)

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(stateTTL),
	})
	redirectURI := s.cfg.baseURL + "/auth/callback"
	http.Redirect(w, r, auth.AuthorizeURL(s.cfg.clientID, redirectURI, state), http.StatusFound)
}

func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookieVal, err := r.Cookie(stateCookie)
	if err != nil || stateCookieVal.Value == "" || stateCookieVal.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid or missing OAuth state", http.StatusBadRequest)
		return
	}
	clearCookie(w, stateCookie, s.secureCookies())

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	token, err := auth.ExchangeCode(ctx, s.httpClient, s.cfg.clientID, s.cfg.clientSecret, code)
	if err != nil {
		http.Error(w, fmt.Sprintf("github login failed: %s", err), http.StatusBadGateway)
		return
	}
	login, err := auth.CurrentUser(ctx, s.httpClient, token)
	if err != nil {
		http.Error(w, fmt.Sprintf("github login failed: %s", err), http.StatusBadGateway)
		return
	}
	permission, err := auth.CollaboratorPermission(ctx, s.httpClient, token, s.cfg.repo, login)
	if err != nil {
		http.Error(w, fmt.Sprintf("github permission check failed: %s", err), http.StatusBadGateway)
		return
	}

	sess := auth.NewSession(login, permission, auth.TTL)
	if !s.authorized(sess) {
		http.Error(w, fmt.Sprintf("forbidden: %s does not have write access to %s", login, s.cfg.repo), http.StatusForbidden)
		return
	}

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
		Expires:  time.Now().Add(auth.TTL),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, sessionCookie, s.secureCookies())
	http.Redirect(w, r, "/", http.StatusFound)
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
