package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
)

// TestSampleDataStillMatchesCommittedManifest is the central assertion behind
// splitting payment out of the invoice documents and behind pinning issued
// PDFs to their own bytes: the committed build/render-manifest.json must
// match the committed build/ artifacts exactly as they stand, so
// `pocket-cfo-ctl render` has nothing to re-render and no issued invoice is
// ever flagged stale by a rendering change.
//
// The two kinds of entry are judged by their own kind of drift:
// an issued entry pins the PDF's bytes, a draft entry pins the rendered
// HTML. If this fails, an artifact on disk no longer matches its pin and
// the change was not transparent.
func TestSampleDataStillMatchesCommittedManifest(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(wd, "..", ".."))

	manifest, err := render.LoadManifest(renderManifestPath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	invoices, err := stats.LoadInvoices(invoicesDir)
	if err != nil {
		t.Fatal(err)
	}
	paid, err := stats.LoadPaid(paidInvoicesPath)
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, inv := range invoices {
		totals, err := money.Compute(inv)
		if err != nil {
			t.Fatalf("compute totals for %s: %v", inv.Number, err)
		}
		var paidOn *types.SerializableDate
		if d, ok := paid[inv.Number]; ok {
			paidOn = &d
		}
		for _, tgt := range targetsFor(*inv, paidOn) {
			name := filepath.Base(tgt.path)
			want, ok := manifest[name]
			if !ok {
				continue // never rendered, so nothing is pinned for it
			}
			if inv.Status != invoice.InvoiceJsonStatusDraft {
				got, err := render.HashPDFFile(tgt.path)
				if err != nil {
					t.Fatalf("hash %s: %v", tgt.path, err)
				}
				if got != want.PDFHash {
					t.Errorf("%s: pdf hash %s, manifest says %s — the stored PDF drifted from its pin", name, got, want.PDFHash)
				}
				checked++
				continue
			}
			html, err := render.HTML(inv, totals, tgt.paidOn)
			if err != nil {
				t.Fatalf("render %s: %v", name, err)
			}
			if got := render.HashHTML(html); got != want.HTMLHash {
				t.Errorf("%s: html hash %s, manifest says %s — the rendered HTML changed, so this draft would need re-rendering", name, got, want.HTMLHash)
			}
			checked++
		}
	}

	// Guards against the test quietly passing because it compared nothing —
	// e.g. if the sample data or the manifest were emptied.
	if checked < 2 {
		t.Fatalf("only %d artifact(s) checked against the manifest, want at least the original and the paid variant", checked)
	}
}

// TestRenderOne_BackfillsManifestWithoutTouchingExistingPDF covers the
// one-time bootstrap this repo needed the moment the integrity-manifest
// feature landed: 3 already-issued invoices had PDFs on disk with no
// manifest entry at all (they predate the feature). renderOne must record a
// baseline hash for them from the current JSON — without re-rendering or
// otherwise touching the existing PDF file.
func TestRenderOne_BackfillsManifestWithoutTouchingExistingPDF(t *testing.T) {
	// render.HTML reads templates/invoice.html.tmpl relative to the repo
	// root, so the temp dir needs its own copy before chdir'ing — same
	// constraint as internal/render's own fixture-loading tests.
	//
	// Copied rather than symlinked: creating a symlink on Windows needs
	// Developer Mode or an elevated process, so a symlink here failed the
	// test on every ordinary Windows checkout. The directory is four small
	// templates, and the test only reads them.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(wd, "..", "..")
	tmp := t.TempDir()
	if err := os.CopyFS(filepath.Join(tmp, "templates"), os.DirFS(filepath.Join(repoRoot, "templates"))); err != nil {
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
	if err := renderOne(context.Background(), nil, "INV-0000000001", false, false, manifest, nil); err != nil {
		t.Fatalf("renderOne: %v", err)
	}

	got, ok := manifest["INV-0000000001.pdf"]
	if !ok {
		t.Error("expected a backfilled manifest entry for INV-0000000001.pdf")
	}
	want, err := render.HashPDFFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.PDFHash != want {
		t.Errorf("backfilled entry = %+v, want it pinned to the stored PDF's bytes (%s)", got, want)
	}

	after, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Error("the pre-existing PDF file was modified during backfill — it must be left untouched")
	}
}
