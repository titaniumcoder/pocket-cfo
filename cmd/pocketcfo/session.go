package main

import (
	"net/http"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
)

const sessionCookie = "pocketcfo_session"

func (s *server) isProd() bool {
	return s.cfg.env == "prod"
}

// currentSession is the single point every auth-gated handler goes
// through. Outside of prod it short-circuits to a synthetic
// always-authorized session — GitHub OAuth is only enforced when
// ENV=prod, so local development never needs a registered OAuth App or a
// browser login. See ARCHITECTURE.md §8 and the ENV=prod note in README.md.
func (s *server) currentSession(r *http.Request) (auth.Session, bool) {
	if !s.isProd() {
		return auth.Session{Login: "local-dev", Permission: "admin"}, true
	}

	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return auth.Session{}, false
	}
	sess, err := auth.Decode(s.cfg.sessionSecret, c.Value)
	if err != nil {
		return auth.Session{}, false
	}
	return sess, true
}

// authorized is the full/write-eligible tier: GitHub collaborators with
// push or admin on the data repo. See readOnly and authenticated for the
// email-login tier alongside it.
func (s *server) authorized(sess auth.Session) bool {
	return sess.Permission == "push" || sess.Permission == "admin"
}

// readOnly is the email-login tier — see handleEmailLoginCallback. It grants
// dashboard viewing but not the portal-link column (see portalLinks), which
// stays exclusive to authorized().
func (s *server) readOnly(sess auth.Session) bool {
	return sess.Permission == "readonly"
}

// authenticated is either tier — the gate for handleIndex/handleInvoicePDF,
// which show the same read-only dashboard content to both.
func (s *server) authenticated(sess auth.Session) bool {
	return s.authorized(sess) || s.readOnly(sess)
}

func (s *server) secureCookies() bool {
	return strings.HasPrefix(s.cfg.baseURL, "https://")
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
