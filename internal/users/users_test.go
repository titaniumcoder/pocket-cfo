package users

import (
	"os"
	"path/filepath"
	"testing"
)

func writeUsersFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write users.json: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeUsersFile(t, `{
		"users": [
			{"email": "Finance@Example.com", "parts": ["finance"]},
			{"email": "both@example.com", "parts": ["finance", "invoicing"]}
		]
	}`)

	u, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(u.Users) != 2 {
		t.Fatalf("got %d users, want 2", len(u.Users))
	}
}

func TestLoad_missingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestLoad_rejectsUnknownPart(t *testing.T) {
	path := writeUsersFile(t, `{"users": [{"email": "a@example.com", "parts": ["accounting"]}]}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an unrecognized part, got nil")
	}
}

func TestPartsFor(t *testing.T) {
	path := writeUsersFile(t, `{
		"users": [
			{"email": "Finance@Example.com", "parts": ["finance"]}
		]
	}`)
	u, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	parts, ok := PartsFor(u, "  finance@example.com ")
	if !ok {
		t.Fatal("expected a case/whitespace-insensitive match, got none")
	}
	if len(parts) != 1 || parts[0] != PartFinance {
		t.Fatalf("got parts %v, want [%s]", parts, PartFinance)
	}

	if _, ok := PartsFor(u, "nobody@example.com"); ok {
		t.Fatal("expected no match for an unlisted email")
	}
}

func TestHasPart(t *testing.T) {
	path := writeUsersFile(t, `{
		"users": [
			{"email": "finance-only@example.com", "parts": ["finance"]},
			{"email": "both@example.com", "parts": ["finance", "invoicing"]}
		]
	}`)
	u, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		name  string
		email string
		part  string
		want  bool
	}{
		{"listed for finance", "finance-only@example.com", PartFinance, true},
		{"not listed for invoicing", "finance-only@example.com", PartInvoicing, false},
		{"listed for both, checking invoicing", "both@example.com", PartInvoicing, true},
		{"unknown email", "nobody@example.com", PartFinance, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasPart(u, tt.email, tt.part); got != tt.want {
				t.Errorf("HasPart(%q, %q) = %v, want %v", tt.email, tt.part, got, tt.want)
			}
		})
	}
}
