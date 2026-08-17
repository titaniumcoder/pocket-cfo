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

func TestRunValidate_RepoData(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if code := runValidate(nil); code != 0 {
		t.Errorf("runValidate = %d over the repo's own data, want 0", code)
	}
}

func repoInvoice(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "data", "invoices", "INV-0000000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func invoiceCheckout(t *testing.T, name string, doc map[string]any) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "catalog", "notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	recipients, err := filepath.Glob(filepath.Join(root, "data", "recipients", "*.json"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	t.Chdir(dir)
	mustMkdirAll(t, "data/invoices")
	mustMkdirAll(t, "data/recipients")
	mustMkdirAll(t, "catalog")
	if err := os.WriteFile(filepath.Join("catalog", "notes.json"), catalog, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range recipients {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("data", "recipients", filepath.Base(p)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, filepath.Join("data", "invoices", name+".json"), doc)
}

func TestRunValidate_EnforcesTheSection43Rules(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		mutate func(map[string]any)
	}{
		{"an unknown key", "INV-0000000001", func(d map[string]any) { d["totl"] = 1 }},
		{"a discount with both percent and amount", "INV-0000000001", func(d map[string]any) {
			d["discounts"] = []any{map[string]any{"label": map[string]any{"de": "R", "bg": "Р"}, "percent": 100, "amount": 100}}
		}},
		{"due before issue", "INV-0000000001", func(d map[string]any) { d["due_date"] = "2020-01-01" }},
		{"a filename that names another invoice", "INV-0000000009", func(d map[string]any) {}},
		{"a regime that contradicts the recipient", "INV-0000000001", func(d map[string]any) {
			d["tax"].(map[string]any)["regime"] = "eu_b2b_reverse_charge"
		}},
		{"a de description with no bg sibling", "INV-0000000001", func(d map[string]any) {
			line := d["lines"].([]any)[0].(map[string]any)
			delete(line["description"].(map[string]any), "bg")
		}},
		{"a discount below zero", "INV-0000000001", func(d map[string]any) {
			d["discounts"] = []any{map[string]any{"label": map[string]any{"de": "R", "bg": "Р"}, "amount": 99999999}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := repoInvoice(t)
			tt.mutate(doc)
			invoiceCheckout(t, tt.file, doc)
			if code := runValidate(nil); code == 0 {
				t.Error("runValidate = 0, want a nonzero exit")
			}
		})
	}
}

func TestRunValidate_RequiresTheMandatoryWording(t *testing.T) {
	doc := repoInvoice(t)
	doc["recipient"] = map[string]any{
		"number": 2, "legal_name": "Beispiel GmbH", "email": "b@example.com",
		"is_business": true, "language": "de", "payment_terms_days": 7, "vat_id": "ATU00000000",
		"address": map[string]any{"line1": "Gasse 2", "postal_code": "1010", "city": "Wien", "country_code": "AT"},
	}
	tax := doc["tax"].(map[string]any)
	tax["regime"] = "eu_b2b_reverse_charge"
	tax["note"] = map[string]any{
		"de": "Steuerschuldnerschaft des Leistungsempfängers.",
		"bg": "Данъкът не е начислен на основание чл. 21, ал. 2 ЗДДС.",
	}
	invoiceCheckout(t, "INV-0000000001", doc)

	if code := runValidate(nil); code == 0 {
		t.Error("runValidate = 0, want a nonzero exit for a reverse-charge invoice that never says so")
	}
}

func TestRunValidate_RefusesASequenceGap(t *testing.T) {
	doc, third := repoInvoice(t), repoInvoice(t)
	third["number"] = "INV-0000000003"

	invoiceCheckout(t, "INV-0000000001", doc)
	writeJSON(t, filepath.Join("data", "invoices", "INV-0000000003.json"), third)

	if code := runValidate(nil); code == 0 {
		t.Error("runValidate = 0, want a nonzero exit for a missing INV-0000000002")
	}
}

func TestRunValidate_InvoicesWithoutACatalogAreReported(t *testing.T) {
	doc := repoInvoice(t)
	invoiceCheckout(t, "INV-0000000001", doc)
	if err := os.Remove(filepath.Join("catalog", "notes.json")); err != nil {
		t.Fatal(err)
	}
	if code := runValidate(nil); code == 0 {
		t.Error("runValidate = 0, want a nonzero exit when there are invoices but no catalog")
	}
}
