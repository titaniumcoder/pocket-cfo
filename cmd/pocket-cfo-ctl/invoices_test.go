package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyInvoice is an invoice file as it looked before the split: `paid` and
// `annulment` still present, in the hand-written key order the real data
// checkout uses.
func legacyInvoice(number, paid, annulment string) string {
	return `{
  "schema_version": 1,
  "number": "` + number + `",
  "status": "issued",
  "type": "invoice",
  "title": "Example",
  "issue_date": "2026-01-05",
  "due_date": "2026-01-19",
  "currency": "EUR",
  "language": "de",
  "lines": [
    {
      "description": { "de": "Arbeit", "bg": "Работа" },
      "unit_price": 10000,
      "vat_rate": 0
    }
  ],
  "paid": ` + paid + `,
  "annulment": ` + annulment + `
}
`
}

func writeRawFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	invoices := filepath.Join(dir, "invoices")
	mustMkdirAll(t, invoices)

	// Deliberately out of sorted order on disk, to pin that the output is
	// sorted by invoice number rather than by directory order.
	writeRawFile(t, filepath.Join(invoices, "INV-0000000002.json"),
		legacyInvoice("INV-0000000002", `"2026-01-28"`, "null"))
	writeRawFile(t, filepath.Join(invoices, "INV-0000000001.json"),
		legacyInvoice("INV-0000000001", `"2026-01-15"`, "null"))
	writeRawFile(t, filepath.Join(invoices, "INV-0000000003.json"),
		legacyInvoice("INV-0000000003", "null", `{"date":"2026-01-02","reason_de":"r","reason_bg":"r"}`))
	return dir
}

func TestExtractPaid(t *testing.T) {
	dir := writeLegacyDataDir(t)

	if code := runExtractPaid([]string{dir}); code != 0 {
		t.Fatalf("runExtractPaid = %d, want 0", code)
	}

	b, err := os.ReadFile(filepath.Join(dir, "paid-invoices.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got extractedPaidFile
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("generated paid-invoices.json does not parse: %v", err)
	}

	want := []extractedPayment{
		{Invoice: "INV-0000000001", Date: "2026-01-15"},
		{Invoice: "INV-0000000002", Date: "2026-01-28"},
	}
	if len(got.Paid) != len(want) {
		t.Fatalf("got %d payments, want %d (the annulment-only invoice was never paid)", len(got.Paid), len(want))
	}
	for i, w := range want {
		if got.Paid[i] != w {
			t.Errorf("payment %d = %+v, want %+v (sorted by invoice number)", i, got.Paid[i], w)
		}
	}
	if got.Schema == "" {
		t.Error("generated file has no $schema pointer")
	}

	// Every invoice file loses both keys and keeps everything else, in order.
	for _, number := range []string{"INV-0000000001", "INV-0000000002", "INV-0000000003"} {
		path := filepath.Join(dir, "invoices", number+".json")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, gone := range []string{`"paid"`, `"annulment"`} {
			if strings.Contains(text, gone) {
				t.Errorf("%s still contains %s", number, gone)
			}
		}
		var parsed map[string]any
		if err := json.Unmarshal(b, &parsed); err != nil {
			t.Fatalf("%s no longer parses as JSON: %v\n%s", number, err, text)
		}
		if parsed["number"] != number {
			t.Errorf("%s: number = %v, want %s", number, parsed["number"], number)
		}
		if parsed["title"] != "Example" {
			t.Errorf("%s: title was lost (= %v)", number, parsed["title"])
		}
		if lines, ok := parsed["lines"].([]any); !ok || len(lines) != 1 {
			t.Errorf("%s: lines were lost (= %v)", number, parsed["lines"])
		}
		// Key order is preserved, so the migration diff is two deleted lines
		// rather than a whole-file reshuffle.
		if i, j := strings.Index(text, `"schema_version"`), strings.Index(text, `"number"`); i > j {
			t.Errorf("%s: key order was not preserved:\n%s", number, text)
		}
	}
}

func TestExtractPaid_RefusesToClobberExistingFile(t *testing.T) {
	dir := writeLegacyDataDir(t)

	if code := runExtractPaid([]string{dir}); code != 0 {
		t.Fatalf("first run = %d, want 0", code)
	}
	before, err := os.ReadFile(filepath.Join(dir, "paid-invoices.json"))
	if err != nil {
		t.Fatal(err)
	}

	if code := runExtractPaid([]string{dir}); code == 0 {
		t.Error("second run = 0, want non-zero — by then paid-invoices.json is the only record of those dates")
	}

	after, err := os.ReadFile(filepath.Join(dir, "paid-invoices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the refused run modified paid-invoices.json anyway")
	}
}

// TestExtractPaid_LeavesAlreadyMigratedFilesUntouched covers re-running
// against a checkout that's partly migrated: a file with neither key must not
// be rewritten at all, not even reformatted.
func TestExtractPaid_LeavesAlreadyMigratedFilesUntouched(t *testing.T) {
	dir := t.TempDir()
	invoices := filepath.Join(dir, "invoices")
	mustMkdirAll(t, invoices)

	migrated := `{
  "schema_version": 1,
  "number": "INV-0000000004",
  "status": "issued",
  "title": "Already migrated"
}
`
	path := filepath.Join(invoices, "INV-0000000004.json")
	writeRawFile(t, path, migrated)

	if code := runExtractPaid([]string{dir}); code != 0 {
		t.Fatalf("runExtractPaid = %d, want 0", code)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != migrated {
		t.Errorf("an already-migrated file was rewritten:\ngot:\n%s\nwant:\n%s", b, migrated)
	}
}

func TestSplitTopLevelRoundTripsUnchanged(t *testing.T) {
	src := legacyInvoice("INV-0000000001", `"2026-01-15"`, "null")

	members, tail, err := splitTopLevel([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(rebuildWithout(members, tail)); got != src {
		t.Errorf("dropping nothing must reproduce the input byte for byte:\ngot:\n%s\nwant:\n%s", got, src)
	}

	t.Run("dropping the first key stays valid JSON", func(t *testing.T) {
		got := rebuildWithout(members, tail, "schema_version")
		var parsed map[string]any
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("result does not parse: %v\n%s", err, got)
		}
		if _, ok := parsed["schema_version"]; ok {
			t.Error("schema_version survived")
		}
		if parsed["number"] != "INV-0000000001" {
			t.Errorf("number = %v, want it kept", parsed["number"])
		}
	})
}
