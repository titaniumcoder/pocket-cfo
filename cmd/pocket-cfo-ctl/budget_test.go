package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// legacyBudget is budget.json as it looks before the migration: no ids, and
// the deliberate hand-maintained formatting — one category per line, keys in
// a chosen order, a nested overrides array — that the rewrite must preserve.
const legacyBudget = `{
  "$schema": "../internal/finance/data/budget.schema.json",
  "groups": [
    {
      "name": "Housing",
      "kind": "private",
      "categories": [
        { "name": "Rent", "amount": 900 },
        { "name": "Internet + Phone", "amount": 45 }
      ]
    },
    {
      "name": "Company - Operations",
      "kind": "company",
      "categories": [
        { "name": "Diverse (Company)", "amount": 150, "overrides": [{ "month": "2026-08-01", "amount": 0 }], "note": "Paused" }
      ]
    }
  ],
  "loans": [
    { "name": "Mom", "amount": 650000 }
  ]
}
`

func writeBudget(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "budget.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readBudget(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "budget.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// uuidRE is the shape budget.schema.json enforces.
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNewUUID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatal(err)
		}
		if !uuidRE.MatchString(id) {
			t.Fatalf("newUUID() = %q, which budget.schema.json would reject", id)
		}
		if seen[id] {
			t.Fatalf("newUUID() repeated %q", id)
		}
		seen[id] = true
	}
}

func TestBudgetIDs(t *testing.T) {
	dir := writeBudget(t, legacyBudget)

	if code := runBudgetIDs([]string{dir}); code != 0 {
		t.Fatalf("runBudgetIDs = %d, want 0", code)
	}
	got := readBudget(t, dir)

	ids := regexp.MustCompile(`"id": "([^"]+)"`).FindAllStringSubmatch(got, -1)
	if len(ids) != 3 {
		t.Fatalf("got %d ids, want 3:\n%s", len(ids), got)
	}
	unique := map[string]bool{}
	for _, m := range ids {
		if !uuidRE.MatchString(m[1]) {
			t.Errorf("id %q is not a UUID", m[1])
		}
		unique[m[1]] = true
	}
	if len(unique) != 3 {
		t.Errorf("ids are not unique: %v", ids)
	}
	// Each id sits immediately before its category's name, not somewhere else.
	for _, name := range []string{"Rent", "Internet + Phone", "Diverse (Company)"} {
		if !regexp.MustCompile(`"id": "[0-9a-f-]+", "name": "` + regexp.QuoteMeta(name) + `"`).MatchString(got) {
			t.Errorf("%q did not get an id in front of it:\n%s", name, got)
		}
	}

	// Formatting is preserved: still one category per line, ids inserted
	// ahead of the existing keys rather than the file being re-marshalled.
	if strings.Count(got, "\n") != strings.Count(legacyBudget, "\n") {
		t.Errorf("line count changed from %d to %d — the file was reformatted:\n%s",
			strings.Count(legacyBudget, "\n"), strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, `"overrides": [{ "month": "2026-08-01", "amount": 0 }]`) {
		t.Error("the nested overrides array was reformatted")
	}
	if !strings.Contains(got, `"$schema": "../internal/finance/data/budget.schema.json"`) {
		t.Error("the $schema pointer was lost or moved")
	}
	// Loans are not categories and must not gain an id.
	if !strings.Contains(got, `{ "name": "Mom", "amount": 650000 }`) {
		t.Error("the loan entry was modified")
	}
}

func TestBudgetIDsIsIdempotent(t *testing.T) {
	dir := writeBudget(t, legacyBudget)

	if code := runBudgetIDs([]string{dir}); code != 0 {
		t.Fatalf("first run = %d, want 0", code)
	}
	first := readBudget(t, dir)

	if code := runBudgetIDs([]string{dir}); code != 0 {
		t.Fatalf("second run = %d, want 0", code)
	}
	if second := readBudget(t, dir); second != first {
		t.Errorf("second run changed the file:\n%s", second)
	}
}

// TestBudgetIDsKeepsExistingIDs pins the promise an id makes: it outlives a
// rename, so a category that already has one is never re-derived from its
// current name.
func TestBudgetIDsKeepsExistingIDs(t *testing.T) {
	dir := writeBudget(t, `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "11111111-1111-4111-8111-111111111111", "name": "Renamed Since", "amount": 900 },
      { "name": "New One", "amount": 45 }
    ]}
  ]
}
`)
	if code := runBudgetIDs([]string{dir}); code != 0 {
		t.Fatalf("runBudgetIDs = %d, want 0", code)
	}
	got := readBudget(t, dir)
	if !strings.Contains(got, `"id": "11111111-1111-4111-8111-111111111111"`) {
		t.Errorf("an existing id was rewritten — an id must outlive a rename:\n%s", got)
	}
	if n := len(regexp.MustCompile(`"id": "`).FindAllString(got, -1)); n != 2 {
		t.Errorf("got %d ids, want 2:\n%s", n, got)
	}
}

// TestBudgetIDsGivesIdenticalNamesDistinctIDs covers what a name-derived id
// had to special-case: "Hotel" under two similarly-named groups. Nothing about
// the category feeds the id, so there is nothing to disambiguate.
func TestBudgetIDsGivesIdenticalNamesDistinctIDs(t *testing.T) {
	dir := writeBudget(t, `{
  "groups": [
    { "name": "Trip!", "kind": "private", "categories": [{ "name": "Hotel", "amount": 200 }]},
    { "name": "Trip?", "kind": "private", "categories": [{ "name": "Hotel", "amount": 300 }]}
  ]
}
`)
	if code := runBudgetIDs([]string{dir}); code != 0 {
		t.Fatalf("runBudgetIDs = %d, want 0", code)
	}
	got := readBudget(t, dir)
	ids := regexp.MustCompile(`"id": "([^"]+)"`).FindAllStringSubmatch(got, -1)
	if len(ids) != 2 || ids[0][1] == ids[1][1] {
		t.Errorf("want two distinct ids, got %v", ids)
	}
}

func TestBudgetIDsDryRunWritesNothing(t *testing.T) {
	dir := writeBudget(t, legacyBudget)
	if code := runBudgetIDs([]string{dir, "--dry-run"}); code != 0 {
		t.Fatalf("runBudgetIDs --dry-run = %d, want 0", code)
	}
	if readBudget(t, dir) != legacyBudget {
		t.Error("--dry-run modified the file")
	}
}

// TestBudgetIDsResultIsValid is the self-check that makes byte surgery safe:
// the rewritten file must satisfy the real schema and the real business rules.
func TestBudgetIDsResultIsValid(t *testing.T) {
	dir := writeBudget(t, legacyBudget)
	if code := runBudgetIDs([]string{dir}); code != 0 {
		t.Fatalf("runBudgetIDs = %d, want 0", code)
	}
	if code := runValidate([]string{dir}); code != 0 {
		t.Errorf("validate = %d, want 0 — the migrated file must pass validation", code)
	}
}

func TestVerifyOnlyIDsChangedCatchesTampering(t *testing.T) {
	before := []byte(`{"groups":[{"name":"A","kind":"private","categories":[{"name":"Rent","amount":900}]}]}`)
	tampered := []byte(`{"groups":[{"name":"A","kind":"private","categories":[{"id":"22222222-2222-4222-8222-222222222222","name":"Rent","amount":950}]}]}`)
	if err := verifyOnlyIDsChanged(before, tampered); err == nil {
		t.Error("verifyOnlyIDsChanged accepted a changed amount")
	}

	clean := []byte(`{"groups":[{"name":"A","kind":"private","categories":[{"id":"22222222-2222-4222-8222-222222222222","name":"Rent","amount":900}]}]}`)
	if err := verifyOnlyIDsChanged(before, clean); err != nil {
		t.Errorf("verifyOnlyIDsChanged rejected an id-only change: %v", err)
	}
}

func TestScanCategoriesFindsGroupNameDeclaredAfterCategories(t *testing.T) {
	// "name" may follow "categories" in the file; the group name is
	// back-filled, so id derivation must not depend on key order.
	cats, err := scanCategories([]byte(`{
  "groups": [
    { "categories": [{ "name": "Rent", "amount": 900 }], "kind": "private", "name": "Housing" }
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 1 {
		t.Fatalf("len(cats) = %d, want 1", len(cats))
	}
	if cats[0].group != "Housing" || cats[0].name != "Rent" {
		t.Errorf("got group=%q name=%q, want Housing/Rent", cats[0].group, cats[0].name)
	}
}

func TestBudgetUnknownSubcommand(t *testing.T) {
	if code := runBudget([]string{"nope"}); code == 0 {
		t.Error("runBudget(nope) = 0, want non-zero")
	}
	if code := runBudget(nil); code == 0 {
		t.Error("runBudget() = 0, want non-zero")
	}
}

// TestBudgetIDsRejectsMalformedJSON keeps the failure legible rather than
// producing a half-edited file.
func TestBudgetIDsRejectsMalformedJSON(t *testing.T) {
	dir := writeBudget(t, `{"groups": [`)
	if code := runBudgetIDs([]string{dir}); code == 0 {
		t.Error("runBudgetIDs = 0 on malformed JSON, want non-zero")
	}
}

func TestInsertIDsPreservesMultilineFormatting(t *testing.T) {
	src := []byte(`{
  "groups": [
    {
      "name": "Housing",
      "kind": "private",
      "categories": [
        {
          "name": "Rent",
          "amount": 900
        }
      ]
    }
  ]
}
`)
	cats, err := scanCategories(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := assignIDs(cats); err != nil {
		t.Fatal(err)
	}
	out := string(insertIDs(src, cats))
	if !regexp.MustCompile("\\{\n          \"id\": \"[0-9a-f-]+\",\n          \"name\": \"Rent\"").MatchString(out) {
		t.Errorf("indentation was not matched:\n%s", out)
	}
	var doc any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
}
