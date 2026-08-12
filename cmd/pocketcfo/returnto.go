package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// returnToCookie remembers where an unauthenticated visitor was heading, so
// logging in puts them back there rather than on the dashboard. A link to a
// month, or to a category on the spending page, is worth nothing if following
// it after a session expires lands you somewhere else.
//
// A cookie rather than a query parameter: the value never has to survive a
// round trip through GitHub, and nothing user-controlled ends up in a URL that
// gets redirected to. SameSite=Lax so it still arrives on the top-level
// navigation back from the provider.
const returnToCookie = "pocketcfo_return_to"

// returnToTTL is short on purpose. This is only ever meant to bridge one login,
// and a stale destination sending you somewhere unexpected an hour later is
// worse than landing on the dashboard.
const returnToTTL = 15 * time.Minute

// rememberDestination records the address a visitor was refused, to be used
// once by the next successful login. Call it wherever an unauthenticated
// request is turned away.
func (s *server) rememberDestination(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		return // a form post is not somewhere to send anyone back to
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

// destinationAfterLogin consumes what rememberDestination stored, falling back
// to the dashboard. Consumed rather than read: it describes one interrupted
// navigation, and leaving it set would redirect the next login too.
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

// safeReturnTo accepts only an address on this site.
//
// The rule that matters is the second one: "//evil.example" is a
// protocol-relative URL, which a browser resolves to a different origin
// entirely while still starting with a slash. Checking for a leading slash and
// stopping there is the open-redirect bug in full, and a login flow is exactly
// where it is worth having.
func safeReturnTo(dest string) bool {
	if dest == "" || len(dest) > 512 {
		return false
	}
	if !strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "//") {
		return false
	}
	// A backslash is treated as a slash by some browsers, so /\evil.example
	// is the same trick spelled differently.
	if strings.ContainsAny(dest, "\\\r\n\x00\t") {
		return false
	}
	u, err := url.Parse(dest)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return false
	}
	// Sending someone back into the login flow loops.
	return !strings.HasPrefix(u.Path, "/auth/")
}
