package schemas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func invoiceDoc(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "data", "invoices", "INV-0000000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValidateEnforcesWhatTheGeneratorIgnores(t *testing.T) {
	t.Run("additionalProperties: an unknown key at the root", func(t *testing.T) {
		doc := invoiceDoc(t)
		doc["totl"] = 1
		assertRejected(t, Invoice, mustJSON(t, doc))
	})

	t.Run("additionalProperties: an unknown key inside a nested object", func(t *testing.T) {
		doc := invoiceDoc(t)
		doc["recipient"].(map[string]any)["vat_number"] = "ATU00000000"
		assertRejected(t, Invoice, mustJSON(t, doc))
	})

	t.Run("oneOf: a discount carrying both percent and amount", func(t *testing.T) {
		doc := invoiceDoc(t)
		doc["discounts"] = []any{map[string]any{
			"label":   map[string]any{"de": "Rabatt", "bg": "Rabatt"},
			"percent": 1000,
			"amount":  10000,
		}}
		assertRejected(t, Invoice, mustJSON(t, doc))
	})

	t.Run("oneOf: a discount carrying neither", func(t *testing.T) {
		doc := invoiceDoc(t)
		doc["discounts"] = []any{map[string]any{
			"label": map[string]any{"de": "Rabatt", "bg": "Rabatt"},
		}}
		assertRejected(t, Invoice, mustJSON(t, doc))
	})

	t.Run("const: a schema_version from the future", func(t *testing.T) {
		doc := invoiceDoc(t)
		doc["schema_version"] = 2
		assertRejected(t, Invoice, mustJSON(t, doc))
	})

	t.Run("propertyNames: a misspelled regime key in the catalog", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("..", "catalog", "notes.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		regimes := doc["regimes"].(map[string]any)
		regimes["eu_b2b_reverse_charg"] = regimes["eu_b2b_reverse_charge"]
		delete(regimes, "eu_b2b_reverse_charge")
		assertRejected(t, Notes, mustJSON(t, doc))
	})

	t.Run("uniqueItems: the same user twice", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("..", "data", "users.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		users := doc["users"].([]any)
		doc["users"] = append(users, users[0])
		assertRejected(t, Users, mustJSON(t, doc))
	})
}

func TestValidateAcceptsTheRepoData(t *testing.T) {
	cases := []struct {
		id   ID
		glob string
	}{
		{Invoice, filepath.Join("..", "data", "invoices", "*.json")},
		{Recipient, filepath.Join("..", "data", "recipients", "*.json")},
		{Issuer, filepath.Join("..", "data", "issuer.json")},
		{Users, filepath.Join("..", "data", "users.json")},
		{PaidInvoices, filepath.Join("..", "data", "paid-invoices.json")},
		{Notes, filepath.Join("..", "catalog", "notes.json")},
	}
	for _, c := range cases {
		paths, err := filepath.Glob(c.glob)
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Errorf("%s: %s matched no files", c.id, c.glob)
		}
		for _, p := range paths {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(c.id, raw); err != nil {
				t.Errorf("%s: %v", p, err)
			}
		}
	}
}

func TestValidateEnforcesTheOrdinaryKeywords(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"pattern: a number that is not INV-##########", func(d map[string]any) { d["number"] = "GARBAGE" }},
		{"enum: a status that does not exist", func(d map[string]any) { d["status"] = "issuedd" }},
		{"maximum: a VAT rate over 100", func(d map[string]any) {
			d["lines"].([]any)[0].(map[string]any)["vat_rate"] = 250
		}},
		{"minimum: a negative unit price", func(d map[string]any) {
			d["lines"].([]any)[0].(map[string]any)["unit_price"] = -500
		}},
		{"required: no tax block at all", func(d map[string]any) { delete(d, "tax") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := invoiceDoc(t)
			tt.mutate(doc)
			assertRejected(t, Invoice, mustJSON(t, doc))
		})
	}
}

func TestValidateUnknownSchema(t *testing.T) {
	if err := Validate(ID("nope.json"), []byte(`{}`)); err == nil {
		t.Error("want an error for a schema that is not embedded")
	}
}

func assertRejected(t *testing.T, id ID, raw []byte) {
	t.Helper()
	err := Validate(id, raw)
	if err == nil {
		t.Fatalf("%s accepted a document it should have refused", id)
	}
	if !strings.Contains(err.Error(), string(id)) {
		t.Errorf("error = %q, want it to name the schema", err)
	}
}
