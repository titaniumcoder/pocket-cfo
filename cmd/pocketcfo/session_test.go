package main

import (
	"net/http/httptest"
	"testing"
)

func TestClearCookie(t *testing.T) {
	w := httptest.NewRecorder()
	clearCookie(w, "test_cookie", true)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want exactly 1 cookie set, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != "test_cookie" {
		t.Errorf("Name = %q, want test_cookie", c.Name)
	}
	if c.Value != "" {
		t.Errorf("Value = %q, want empty", c.Value)
	}
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want negative (immediate expiry)", c.MaxAge)
	}
	if !c.Secure {
		t.Error("want Secure=true when secure=true was passed")
	}
}
