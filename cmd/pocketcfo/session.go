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
	return s.cfg.env == envProd
}

func (s *server) authDisabled() bool {
	return s.cfg.env == envDevelopment
}

func (s *server) currentSession(r *http.Request) (auth.Session, bool) {
	if s.authDisabled() {
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

func (s *server) authorized(sess auth.Session) bool {
	return sess.Permission == "push" || sess.Permission == "admin"
}

func (s *server) readOnly(sess auth.Session) bool {
	return sess.Permission == "readonly"
}

func (s *server) authenticated(sess auth.Session) bool {
	return s.authorized(sess) || s.readOnly(sess)
}

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

func (s *server) currentPeriod() webui.Period {
	now := time.Now()
	if s.tracker != nil && s.tracker.Loc != nil {
		now = now.In(s.tracker.Loc)
	}
	return webui.Period{Year: now.Year(), Month: int(now.Month())}
}

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
