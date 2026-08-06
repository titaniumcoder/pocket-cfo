package auth

import (
	"testing"
	"time"
)

func TestLoginToken_RoundTrip(t *testing.T) {
	token, err := GenerateLoginToken("test-secret", "person@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	email, err := VerifyLoginToken("test-secret", token)
	if err != nil {
		t.Fatal(err)
	}
	if email != "person@example.com" {
		t.Errorf("email = %q, want person@example.com", email)
	}
}

func TestLoginToken_Expired(t *testing.T) {
	token, err := GenerateLoginToken("test-secret", "person@example.com", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyLoginToken("test-secret", token); err == nil {
		t.Fatal("expected an error verifying an expired token, got nil")
	}
}

func TestLoginToken_WrongSecret(t *testing.T) {
	token, err := GenerateLoginToken("secret-a", "person@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyLoginToken("secret-b", token); err == nil {
		t.Fatal("expected an error verifying with the wrong secret, got nil")
	}
}

func TestLoginToken_Tampered(t *testing.T) {
	token, err := GenerateLoginToken("test-secret", "person@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tampered := token[:len(token)-4] + "AAAA"
	if _, err := VerifyLoginToken("test-secret", tampered); err == nil {
		t.Fatal("expected an error verifying a tampered token, got nil")
	}
}

func TestLoginToken_Malformed(t *testing.T) {
	if _, err := VerifyLoginToken("test-secret", "not-a-valid-token"); err == nil {
		t.Fatal("expected an error verifying a malformed token, got nil")
	}
}
