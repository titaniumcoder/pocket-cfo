package auth

import (
	"testing"
	"time"
)

func TestLoginToken(t *testing.T) {
	tests := []struct {
		name       string
		token      func(t *testing.T) string
		verifyWith string
		wantErr    bool
		wantEmail  string
	}{
		{
			name: "round trip",
			token: func(t *testing.T) string {
				token, err := GenerateLoginToken("test-secret", "person@example.com", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				return token
			},
			verifyWith: "test-secret",
			wantEmail:  "person@example.com",
		},
		{
			name: "expired",
			token: func(t *testing.T) string {
				token, err := GenerateLoginToken("test-secret", "person@example.com", -time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				return token
			},
			verifyWith: "test-secret",
			wantErr:    true,
		},
		{
			name: "wrong secret",
			token: func(t *testing.T) string {
				token, err := GenerateLoginToken("secret-a", "person@example.com", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				return token
			},
			verifyWith: "secret-b",
			wantErr:    true,
		},
		{
			name: "tampered",
			token: func(t *testing.T) string {
				token, err := GenerateLoginToken("test-secret", "person@example.com", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				return token[:len(token)-4] + "AAAA"
			},
			verifyWith: "test-secret",
			wantErr:    true,
		},
		{
			name:       "malformed",
			token:      func(t *testing.T) string { return "not-a-valid-token" },
			verifyWith: "test-secret",
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, err := VerifyLoginToken(tt.verifyWith, tt.token(t))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error verifying a %s token, got nil", tt.name)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if email != tt.wantEmail {
				t.Errorf("email = %q, want %q", email, tt.wantEmail)
			}
		})
	}
}
