package auth

import (
	"strings"
	"testing"
	"time"
)

func TestEncodeDecode(t *testing.T) {
	tests := []struct {
		name       string
		encoded    func(t *testing.T) string
		verifyWith string
		wantErr    bool
		wantLogin  string
		wantPerm   string
	}{
		{
			name: "round trip",
			encoded: func(t *testing.T) string {
				encoded, err := Encode("test-secret", NewSession("octocat", "push", TTL))
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			},
			verifyWith: "test-secret",
			wantLogin:  "octocat",
			wantPerm:   "push",
		},
		{
			name: "wrong secret",
			encoded: func(t *testing.T) string {
				encoded, err := Encode("secret-a", NewSession("octocat", "push", TTL))
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			},
			verifyWith: "secret-b",
			wantErr:    true,
		},
		{
			name: "expired",
			encoded: func(t *testing.T) string {
				expired := Session{Login: "octocat", Permission: "push", ExpiresAt: time.Now().Add(-time.Minute)}
				encoded, err := Encode("test-secret", expired)
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			},
			verifyWith: "test-secret",
			wantErr:    true,
		},
		{
			name: "tampered",
			encoded: func(t *testing.T) string {
				encoded, err := Encode("test-secret", NewSession("octocat", "push", TTL))
				if err != nil {
					t.Fatal(err)
				}
				return strings.Replace(encoded, encoded[len(encoded)-4:], "AAAA", 1)
			},
			verifyWith: "test-secret",
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode(tt.verifyWith, tt.encoded(t))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error decoding a %s value, got nil", tt.name)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Login != tt.wantLogin || got.Permission != tt.wantPerm {
				t.Errorf("got %+v, want Login=%q Permission=%q", got, tt.wantLogin, tt.wantPerm)
			}
		})
	}
}
