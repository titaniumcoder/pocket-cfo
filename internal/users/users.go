// Package users reads the private users.json access-control file and
// answers "which part(s) of PocketCFO may this email reach". It is only
// consulted for the email-OTP tier — GitHub repo collaborators are always
// full-access admins (see internal/auth) and are never listed in
// users.json at all. Read fresh from disk on every check, same convention
// as internal/stats.LoadRecipients, so an edit takes effect without a
// restart and re-checking at click time (not just request time) sees the
// latest file.
package users

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	schemausers "github.com/titaniumcoder/pocket-cfo/internal/schema/users"
)

// Part names — must match the "parts" enum in schemas/users.json.
const (
	PartFinance   = "finance"
	PartInvoicing = "invoicing"
)

// Load reads and parses the users.json file at path.
func Load(path string) (schemausers.UsersJson, error) {
	var u schemausers.UsersJson
	b, err := os.ReadFile(path)
	if err != nil {
		return u, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &u); err != nil {
		return u, fmt.Errorf("parse %s: %w", path, err)
	}
	return u, nil
}

// PartsFor returns the parts email (matched case-insensitively) is listed
// for, and whether it was found in u at all.
func PartsFor(u schemausers.UsersJson, email string) ([]string, bool) {
	email = normalize(email)
	for _, entry := range u.Users {
		if normalize(entry.Email) == email {
			parts := make([]string, len(entry.Parts))
			for i, p := range entry.Parts {
				parts[i] = string(p)
			}
			return parts, true
		}
	}
	return nil, false
}

// HasPart reports whether email is listed in u with access to part.
func HasPart(u schemausers.UsersJson, email, part string) bool {
	parts, ok := PartsFor(u, email)
	if !ok {
		return false
	}
	return slices.Contains(parts, part)
}

func normalize(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
