package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

const returnToCookie = "pocketcfo_return_to"

const returnToTTL = 15 * time.Minute

func (s *server) rememberDestination(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		return
	}
	dest := r.URL.RequestURI()
	if dest == "/" || !safeReturnTo(dest) {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     returnToCookie,
		Value:    url.QueryEscape(dest),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(returnToTTL),
	})
}

func (s *server) destinationAfterLogin(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(returnToCookie)
	if err != nil || c.Value == "" {
		return "/"
	}
	clearCookie(w, returnToCookie, s.secureCookies())
	dest, err := url.QueryUnescape(c.Value)
	if err != nil || !safeReturnTo(dest) {
		return "/"
	}
	return dest
}

func safeReturnTo(dest string) bool {
	if dest == "" || len(dest) > 512 {
		return false
	}
	if !strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "//") {
		return false
	}
	if strings.ContainsAny(dest, "\\\r\n\x00\t") {
		return false
	}
	u, err := url.Parse(dest)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return false
	}
	return !strings.HasPrefix(u.Path, "/auth/")
}
