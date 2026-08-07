package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/render"
)

// TestRenderOne_BackfillsManifestWithoutTouchingExistingPDF covers the
// one-time bootstrap this repo needed the moment the integrity-manifest
// feature landed: 3 already-issued invoices had PDFs on disk with no
// manifest entry at all (they predate the feature). renderOne must record a
// baseline hash for them from the current JSON — without re-rendering or
// otherwise touching the existing PDF file.
func TestRenderOne_BackfillsManifestWithoutTouchingExistingPDF(t *testing.T) {
	// render.HTML reads web/templates/invoice.html.tmpl relative to the
	// repo root, so the temp dir needs a symlink to it before chdir'ing —
	// same constraint as internal/render's own fixture-loading tests.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(wd, "..", "..")
	tmp := t.TempDir()
	if err := os.Symlink(filepath.Join(repoRoot, "web"), filepath.Join(tmp, "web")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	mustMkdirAll(t, invoicesDir)
	mustMkdirAll(t, buildDir)

	inv := draftInvoiceFixture(t, "INV-0000000001", strp("Arbeit"), nil)
	inv.Status = "issued"                        // renderOne's backfill path only applies to write-once (non-draft) targets
	inv.Lines[0].Description.Bg = strp("Работа") // fixture leaves bg missing by design (translate tests); a
	inv.Tax.Note.Bg = strp("Бележка")            // backfilled invoice must actually be renderable, so fill it in.
	writeInvoiceFixture(t, invoicesDir, inv)

	pdfPath := filepath.Join(buildDir, "INV-0000000001.pdf")
	original := []byte("pre-existing pdf bytes, must not change")
	if err := os.WriteFile(pdfPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := render.Manifest{}
	if err := renderOne(context.Background(), nil, nil, "INV-0000000001", false, false, manifest); err != nil {
		t.Fatalf("renderOne: %v", err)
	}

	got, ok := manifest["INV-0000000001.pdf"]
	if !ok || got == "" {
		t.Error("expected a backfilled manifest entry for INV-0000000001.pdf")
	}

	after, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Error("the pre-existing PDF file was modified during backfill — it must be left untouched")
	}
}
