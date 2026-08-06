package auth

import (
	"strings"
	"testing"
	"time"
)

func TestEncodeDecode_RoundTrip(t *testing.T) {
	s := NewSession("octocat", "push", TTL)

	encoded, err := Encode("test-secret", s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode("test-secret", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != s.Login || got.Permission != s.Permission {
		t.Errorf("got %+v, want %+v", got, s)
	}
}

func TestDecode_WrongSecret(t *testing.T) {
	encoded, err := Encode("secret-a", NewSession("octocat", "push", TTL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode("secret-b", encoded); err == nil {
		t.Fatal("expected an error decoding with the wrong secret, got nil")
	}
}

func TestDecode_Expired(t *testing.T) {
	expired := Session{Login: "octocat", Permission: "push", ExpiresAt: time.Now().Add(-time.Minute)}
	encoded, err := Encode("test-secret", expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode("test-secret", encoded); err == nil {
		t.Fatal("expected an error decoding an expired session, got nil")
	}
}

func TestDecode_Tampered(t *testing.T) {
	encoded, err := Encode("test-secret", NewSession("octocat", "push", TTL))
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(encoded, encoded[len(encoded)-4:], "AAAA", 1)
	if _, err := Decode("test-secret", tampered); err == nil {
		t.Fatal("expected an error decoding a tampered value, got nil")
	}
}
