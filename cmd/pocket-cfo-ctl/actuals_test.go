package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const augustActuals = `{
  "month": "2026-08",
  "coverage": [{ "account": "A", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }],
  "transactions": [
    { "id": "t1", "date": "2026-08-01", "description": "MIETE", "amount": 900, "account": "A", "category": "00000000-0000-4000-8000-000000000001" },
    { "id": "t2", "date": "2026-08-03", "description": "LIDL", "amount": 210.4, "account": "A", "category": "00000000-0000-4000-8000-000000000002" }
  ]
}
`

const budgetWithIDs = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 900 }
    ]},
    { "name": "Food", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000002", "name": "Groceries", "amount": 350 }
    ]}
  ]
}
`

func actualsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "actuals"))
	if err := os.WriteFile(filepath.Join(dir, "budget.json"), []byte(budgetWithIDs), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "actuals", "2026-08.json"), []byte(augustActuals), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestActualsValidate(t *testing.T) {
	dir := actualsDir(t)
	if code := runActuals([]string{"validate", dir}); code != 0 {
		t.Errorf("validate = %d, want 0", code)
	}
}

func TestActualsValidateRejectsAnUnknownCategory(t *testing.T) {
	dir := actualsDir(t)
	bad := `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"t1","date":"2026-08-01","description":"X","amount":10,"account":"A","category":"gone.away"}]}`
	if err := os.WriteFile(filepath.Join(dir, "actuals", "2026-08.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runActuals([]string{"validate", dir}); code == 0 {
		t.Error("validate = 0 for a transaction citing a category that isn't in budget.json")
	}
}

func TestActualsValidateNoMonthsIsFine(t *testing.T) {
	if code := runActuals([]string{"validate", t.TempDir()}); code != 0 {
		t.Error("a data dir with no actuals/ must validate cleanly")
	}
}

func TestActualsStatusAndCategories(t *testing.T) {
	dir := actualsDir(t)
	if code := runActuals([]string{"status", dir}); code != 0 {
		t.Errorf("status = %d, want 0", code)
	}
	if code := runActuals([]string{"categories", dir}); code != 0 {
		t.Errorf("categories = %d, want 0", code)
	}
}

func TestActualsUnknownSubcommand(t *testing.T) {
	if code := runActuals([]string{"nope"}); code == 0 {
		t.Error("runActuals(nope) = 0, want non-zero")
	}
	if code := runActuals(nil); code == 0 {
		t.Error("runActuals() = 0, want non-zero")
	}
}

// gitRepo turns dir into a committed git repository, or skips the test.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "initial"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestActualsValidateBaseRefCatchesARemoval(t *testing.T) {
	dir := actualsDir(t)
	gitRepo(t, dir)

	// The figures still add up, which is why this needs catching.
	shrunk := `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-02","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"t2","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000002"}]}`
	if err := os.WriteFile(filepath.Join(dir, "actuals", "2026-08.json"), []byte(shrunk), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runActuals([]string{"validate", "--base-ref", "HEAD", dir}); code == 0 {
		t.Error("validate = 0, want non-zero — a committed transaction and a covered day both disappeared")
	}
	// The same submission is accepted once a reason is given.
	if code := runActuals([]string{"validate", "--base-ref", "HEAD", "--allow-removals", "duplicate import", dir}); code != 0 {
		t.Errorf("validate with --allow-removals = %d, want 0", code)
	}
}

func TestActualsValidateBaseRefAllowsANewMonth(t *testing.T) {
	dir := actualsDir(t)
	gitRepo(t, dir)

	september := `{"month":"2026-09","coverage":[{"account":"A","from":"2026-09-01","to":"2026-09-30","imported_at":"2026-10-01"}],
		"transactions":[{"id":"s1","date":"2026-09-01","description":"MIETE","amount":900,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`
	if err := os.WriteFile(filepath.Join(dir, "actuals", "2026-09.json"), []byte(september), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runActuals([]string{"validate", "--base-ref", "HEAD", dir}); code != 0 {
		t.Errorf("validate = %d, want 0 — a month that didn't exist at the ref is new, not destroyed", code)
	}
}

func TestActualsValidateBaseRefAllowsAdditions(t *testing.T) {
	dir := actualsDir(t)
	gitRepo(t, dir)

	grown := `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[
			{"id":"t1","date":"2026-08-01","description":"MIETE","amount":900,"account":"A","category":"00000000-0000-4000-8000-000000000001"},
			{"id":"t2","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000002"},
			{"id":"t3","date":"2026-08-20","description":"BILLA","amount":50,"account":"A","category":"00000000-0000-4000-8000-000000000002"}
		]}`
	if err := os.WriteFile(filepath.Join(dir, "actuals", "2026-08.json"), []byte(grown), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runActuals([]string{"validate", "--base-ref", "HEAD", dir}); code != 0 {
		t.Errorf("validate = %d, want 0 — adding transactions is the common case and must never trip the check", code)
	}
}

// TestActualsValidateHonoursTheCommitTrailer covers the path CI uses.
func TestActualsValidateHonoursTheCommitTrailer(t *testing.T) {
	dir := actualsDir(t)
	gitRepo(t, dir)

	shrunk := `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"t2","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000002"}]}`
	if err := os.WriteFile(filepath.Join(dir, "actuals", "2026-08.json"), []byte(shrunk), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t",
		"commit", "-aqm", "fix(actuals): drop a duplicated import\n\nAllow-Removals: week 1 was imported twice")
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	if code := runActuals([]string{"validate", "--base-ref", "HEAD~1", dir}); code != 0 {
		t.Errorf("validate = %d, want 0 — an Allow-Removals trailer is the override CI uses", code)
	}
}
