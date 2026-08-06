package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

func setupDeleteFixture(t *testing.T, number string, withManifestEntry bool) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(invoicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}

	inv := draftInvoiceFixture(t, number, strp("Arbeit"), nil)
	writeInvoiceFixture(t, invoicesDir, inv)

	pdfPath := filepath.Join(buildDir, number+"-DRAFT.pdf")
	if err := os.WriteFile(pdfPath, []byte("pdf bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if withManifestEntry {
		m := render.Manifest{number + "-DRAFT.pdf": "deadbeef"}
		if err := m.Save(renderManifestPath); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunDelete_RemovesDraftJSONPDFAndManifestEntry(t *testing.T) {
	setupDeleteFixture(t, "INV-0000000009", true)

	if code := runDelete([]string{"INV-0000000009"}); code != 0 {
		t.Fatalf("runDelete exit code = %d, want 0", code)
	}

	if _, err := os.Stat(filepath.Join(invoicesDir, "INV-0000000009.json")); !os.IsNotExist(err) {
		t.Errorf("invoice JSON still present: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")); !os.IsNotExist(err) {
		t.Errorf("draft PDF still present: err = %v", err)
	}

	m, err := render.LoadManifest(renderManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["INV-0000000009-DRAFT.pdf"]; ok {
		t.Error("manifest entry for the deleted draft PDF is still present")
	}
}

func TestRunDelete_NoPDFOrManifestYet(t *testing.T) {
	setupDeleteFixture(t, "INV-0000000009", false)
	if err := os.Remove(filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")); err != nil {
		t.Fatal(err)
	}

	if code := runDelete([]string{"INV-0000000009"}); code != 0 {
		t.Fatalf("runDelete exit code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(invoicesDir, "INV-0000000009.json")); !os.IsNotExist(err) {
		t.Errorf("invoice JSON still present: err = %v", err)
	}
}

func TestRunDelete_RefusesIssuedInvoice(t *testing.T) {
	setupDeleteFixture(t, "INV-0000000009", true)

	inv := draftInvoiceFixture(t, "INV-0000000009", strp("Arbeit"), nil)
	inv.Status = invoice.InvoiceJsonStatusIssued
	writeInvoiceFixture(t, invoicesDir, inv)

	if code := runDelete([]string{"INV-0000000009"}); code == 0 {
		t.Fatal("runDelete succeeded against an issued invoice, want refusal")
	}

	if _, err := os.Stat(filepath.Join(invoicesDir, "INV-0000000009.json")); err != nil {
		t.Errorf("invoice JSON was removed despite refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")); err != nil {
		t.Errorf("draft PDF was removed despite refusal: %v", err)
	}
}

func TestRunDelete_DryRunTouchesNothing(t *testing.T) {
	setupDeleteFixture(t, "INV-0000000009", true)

	if code := runDelete([]string{"INV-0000000009", "--dry-run"}); code != 0 {
		t.Fatalf("runDelete --dry-run exit code = %d, want 0", code)
	}

	if _, err := os.Stat(filepath.Join(invoicesDir, "INV-0000000009.json")); err != nil {
		t.Errorf("invoice JSON removed during --dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")); err != nil {
		t.Errorf("draft PDF removed during --dry-run: %v", err)
	}
	m, err := render.LoadManifest(renderManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["INV-0000000009-DRAFT.pdf"]; !ok {
		t.Error("manifest entry removed during --dry-run")
	}
}

func TestRunDelete_RequiresExactlyOneNumber(t *testing.T) {
	t.Chdir(t.TempDir())
	if code := runDelete(nil); code == 0 {
		t.Error("runDelete with no arguments should fail")
	}
	if code := runDelete([]string{"INV-0000000001", "INV-0000000002"}); code == 0 {
		t.Error("runDelete with two invoice numbers should fail")
	}
}
