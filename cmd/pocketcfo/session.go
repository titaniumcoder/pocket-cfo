package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
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

// header builds the shared site header's view for the page being rendered.
// Each Show* flag mirrors that page's own access gate exactly — finance and
// invoicing by users.json part, info by the stricter authorized() tier — so
// the menu can never offer a link to a page the session would be bounced
// from. active is the page currently being viewed (see webui.Page*).
func (s *server) header(sess auth.Session, active string, period webui.Period) webui.Header {
	if period.Year == 0 {
		period = s.currentPeriod()
	}
	return webui.Header{
		Login:         sess.Login,
		Active:        active,
		ShowFinance:   sess.HasPart(users.PartFinance),
		ShowSpending:  s.showSpending(sess),
		ShowInvoicing: sess.HasPart(users.PartInvoicing),
		ShowInfo:      s.authorized(sess),
		Period:        period,
	}
}

// currentPeriod is the fallback for a page reached without one: today, in the
// tracker's own zone, which is what every menu link pointed at before periods
// were carried at all.
func (s *server) currentPeriod() webui.Period {
	now := time.Now()
	if s.tracker != nil && s.tracker.Loc != nil {
		now = now.In(s.tracker.Loc)
	}
	return webui.Period{Year: now.Year(), Month: int(now.Month())}
}

// showSpending mirrors financeSpending's own gate. It carries statement
// descriptions, so it is admin-only like /info; and a deployment with no
// actuals directory has nothing to show, so it gets no entry rather than one
// leading to a permanently empty page.
func (s *server) showSpending(sess auth.Session) bool {
	return s.authorized(sess) && s.tracker != nil && s.tracker.Actuals.Configured()
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
