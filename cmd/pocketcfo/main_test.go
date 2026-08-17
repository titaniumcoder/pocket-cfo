package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
)

func TestCurrentSession_BypassesAuthOnlyInDevelopment(t *testing.T) {
	s := &server{cfg: config{env: "development"}}
	r := httptest.NewRequest(http.MethodGet, "/", nil) // no cookie at all

	sess, ok := s.currentSession(r)
	if !ok {
		t.Fatal("want ok=true in development, got false")
	}
	if !s.authorized(sess) {
		t.Errorf("want an authorized synthetic session, got %+v", sess)
	}
}

func TestCurrentSession_UnknownEnvDoesNotBypassAuth(t *testing.T) {
	for _, env := range []string{"", "production", "Development", "anything-else"} {
		t.Run("env="+env, func(t *testing.T) {
			s := &server{cfg: config{env: env}}
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			if _, ok := s.currentSession(r); ok {
				t.Errorf("ENV=%q served a session with no cookie", env)
			}
		})
	}
}

func TestCurrentSession_RequiresRealSessionInProd(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}

	t.Run("no cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, ok := s.currentSession(r); ok {
			t.Error("want ok=false with no session cookie in prod")
		}
	})

	t.Run("valid cookie", func(t *testing.T) {
		encoded, err := auth.Encode("test-secret", auth.NewSession("octocat", "push", time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})

		sess, ok := s.currentSession(r)
		if !ok {
			t.Fatal("want ok=true with a valid session cookie")
		}
		if sess.Login != "octocat" {
			t.Errorf("Login = %q, want octocat", sess.Login)
		}
	})
}
