package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunValidate_ValidSampleData(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, "data/recipients")
	mustMkdirAll(t, "data/invoices")
	writeJSON(t, "data/recipients/0001.json", map[string]any{
		"number": 1, "legal_name": "Alice Ltd", "email": "alice@example.com",
		"is_business": true, "language": "de", "payment_terms_days": 14,
		"address": map[string]any{"line1": "Street 1", "postal_code": "1000", "city": "Vienna", "country_code": "AT"},
	})
	writeJSON(t, "data/users.json", map[string]any{
		"users": []map[string]any{{"email": "a@example.com", "parts": []string{"finance"}}},
	})
	writeJSON(t, "data/budget.json", map[string]any{
		"groups": []map[string]any{
			{"name": "Housing", "kind": "private", "categories": []map[string]any{
				{"id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 900},
			}},
		},
	})

	if code := runValidate(nil); code != 0 {
		t.Errorf("runValidate = %d, want 0 for valid data", code)
	}
}

func TestRunValidate_InvalidBudgetFails(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, "data")
	// Duplicate category names within the same group are rejected by
	// budgetdata.ValidateBudget, not expressible in the JSON Schema alone.
	writeJSON(t, "data/budget.json", map[string]any{
		"groups": []map[string]any{
			{"name": "Housing", "kind": "private", "categories": []map[string]any{
				// Distinct ids on purpose: this case is about a duplicate
				// *name*, so it must not trip the id check first.
				{"id": "00000000-0000-4000-8000-000000000013", "name": "Rent", "amount": 900},
				{"id": "00000000-0000-4000-8000-000000000014", "name": "Rent", "amount": 100},
			}},
		},
	})

	if code := runValidate(nil); code == 0 {
		t.Error("runValidate = 0, want a nonzero exit for a budget with duplicate category names")
	}
}

func TestRunValidate_MissingOptionalFilesIsFine(t *testing.T) {
	t.Chdir(t.TempDir())
	if code := runValidate(nil); code != 0 {
		t.Errorf("runValidate = %d, want 0 when the data dir is entirely absent", code)
	}
}

func TestRunValidate_MalformedJSONFails(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, "data/recipients")
	if err := os.WriteFile(filepath.Join("data", "recipients", "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runValidate(nil); code == 0 {
		t.Error("runValidate = 0, want a nonzero exit for malformed JSON")
	}
}
