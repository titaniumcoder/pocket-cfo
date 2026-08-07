package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/render"
)

func TestRunPrune_RemovesDraftPDFWithoutJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, invoicesDir)
	mustMkdirAll(t, buildDir)

	// A relic: the JSON is gone (deleted by hand before invoicectl delete
	// existed) but its -DRAFT.pdf and manifest entry survived.
	orphanPDF := filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")
	if err := os.WriteFile(orphanPDF, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := render.Manifest{"INV-0000000009-DRAFT.pdf": "deadbeef"}
	if err := m.Save(renderManifestPath); err != nil {
		t.Fatal(err)
	}

	if code := runPrune(nil); code != 0 {
		t.Fatalf("runPrune exit code = %d, want 0", code)
	}

	if _, err := os.Stat(orphanPDF); !os.IsNotExist(err) {
		t.Errorf("orphaned draft PDF still present: err = %v", err)
	}
	got, err := render.LoadManifest(renderManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["INV-0000000009-DRAFT.pdf"]; ok {
		t.Error("manifest entry for the orphaned draft PDF is still present")
	}
}

func TestRunPrune_LeavesLiveDraftAlone(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, invoicesDir)
	mustMkdirAll(t, buildDir)

	inv := draftInvoiceFixture(t, "INV-0000000009", strp("Arbeit"), nil)
	writeInvoiceFixture(t, invoicesDir, inv)
	pdfPath := filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")
	if err := os.WriteFile(pdfPath, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runPrune(nil); code != 0 {
		t.Fatalf("runPrune exit code = %d, want 0", code)
	}
	if _, err := os.Stat(pdfPath); err != nil {
		t.Errorf("live draft's PDF was removed: %v", err)
	}
}

func TestRunPrune_DryRunTouchesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, invoicesDir)
	mustMkdirAll(t, buildDir)
	orphanPDF := filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")
	if err := os.WriteFile(orphanPDF, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runPrune([]string{"--dry-run"}); code != 0 {
		t.Fatalf("runPrune --dry-run exit code = %d, want 0", code)
	}
	if _, err := os.Stat(orphanPDF); err != nil {
		t.Errorf("--dry-run removed the orphaned PDF: %v", err)
	}
}

func TestRunPrune_NoBuildDirIsANoOp(t *testing.T) {
	t.Chdir(t.TempDir())
	if code := runPrune(nil); code != 0 {
		t.Fatalf("runPrune exit code = %d, want 0 when build/ doesn't exist yet", code)
	}
}

func TestRunPrune_IgnoresNonDraftPDFs(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, invoicesDir)
	mustMkdirAll(t, buildDir)
	// Issued invoice's original PDF, no matching JSON on disk — must never
	// happen in practice (invoicectl delete refuses non-drafts), but prune
	// must not touch it regardless; it only ever considers *-DRAFT.pdf.
	issuedPDF := filepath.Join(buildDir, "INV-0000000001.pdf")
	if err := os.WriteFile(issuedPDF, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runPrune(nil); code != 0 {
		t.Fatalf("runPrune exit code = %d, want 0", code)
	}
	if _, err := os.Stat(issuedPDF); err != nil {
		t.Errorf("non-draft PDF was removed: %v", err)
	}
}
