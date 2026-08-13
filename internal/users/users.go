package users

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	schemausers "github.com/titaniumcoder/pocket-cfo/internal/schema/users"
)

const (
	PartFinance   = "finance"
	PartInvoicing = "invoicing"
)

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
