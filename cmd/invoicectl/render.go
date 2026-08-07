package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/sign"
)

// invoicesDir and buildDir default to the layout described in
// ARCHITECTURE.md §2, overridable via INVOICES_DIR/BUILD_DIR — e.g. to point
// this binary at a data checkout that lives outside its own repo.
// renderManifestPath is duplicated as a separate var in cmd/pocketcfo/main.go
// (a different `main` package) rather than shared, matching the existing
// buildDir/invoicesDir precedent of independently declaring the same
// repo-root-relative paths per binary.
var (
	invoicesDir        = getenv("INVOICES_DIR", "data/invoices")
	buildDir           = getenv("BUILD_DIR", "build")
	renderManifestPath = buildDir + "/render-manifest.json"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// runRender implements `invoicectl render [NUMBER] [--force] [--dry-run]`.
// See ARCHITECTURE.md §5.
func runRender(args []string) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	force := fs.Bool("force", false, "re-render even an already-issued PDF (requires a single invoice number)")
	dryRun := fs.Bool("dry-run", false, "print what would render, without rendering")

	flagArgs, explicit := splitFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if !forceAllowed(*force, explicit) {
		fmt.Fprintln(os.Stderr, "invoicectl render: --force requires exactly one invoice number (bulk force-render is not allowed)")
		return 1
	}

	numbers, err := invoiceNumbers(explicit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invoicectl render:", err)
		return 1
	}
	if len(numbers) == 0 {
		fmt.Fprintln(os.Stderr, "invoicectl render: no invoices found under", invoicesDir)
		return 1
	}

	var renderer render.Renderer
	var signer *sign.PDFSigner
	if !*dryRun {
		apiKey := os.Getenv("API2PDF_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "invoicectl render: API2PDF_KEY is not set (see .envrc.example)")
			return 1
		}
		renderer = render.NewAPI2PDF(apiKey)

		var ok bool
		var err error
		signer, ok, err = sign.NewFromEnv()
		if err != nil {
			fmt.Fprintln(os.Stderr, "invoicectl render:", err)
			return 1
		}
		if !ok {
			signer = nil // no SIGN_CERT_B64 configured — signing is a no-op, see internal/sign
		}
	}

	manifest := render.Manifest{}
	if !*dryRun {
		var err error
		manifest, err = render.LoadManifest(renderManifestPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invoicectl render:", err)
			return 1
		}
	}

	failed := 0
	for _, number := range numbers {
		if err := renderOne(context.Background(), renderer, signer, number, *force, *dryRun, manifest); err != nil {
			fmt.Fprintf(os.Stderr, "invoicectl render: %s: %v\n", number, err)
			failed++
		}
	}

	if !*dryRun {
		if err := manifest.Save(renderManifestPath); err != nil {
			fmt.Fprintln(os.Stderr, "invoicectl render:", err)
			return 1
		}
	}

	if failed > 0 {
		fmt.Fprintf(os.Stderr, "invoicectl render: %d of %d invoice(s) failed\n", failed, len(numbers))
		return 1
	}
	return 0
}

// splitFlags separates args into flag tokens (leading "-") and positional
// ones. flag.Parse stops at the first non-flag argument, so without this,
// "render INV-... --force" (flag after the invoice number, as documented in
// ARCHITECTURE.md §5.2 and README.md) would leave --force unparsed and
// treated as a second invoice number. This makes flag/positional order not
// matter.
func splitFlags(args []string) (flagArgs, positional []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else {
			positional = append(positional, a)
		}
	}
	return flagArgs, positional
}

// forceAllowed reports whether --force may be used with this invocation:
// only against exactly one explicitly named invoice, never a bulk render —
// issued PDFs are written once and kept, see ARCHITECTURE.md §3.
func forceAllowed(force bool, explicit []string) bool {
	return !force || len(explicit) == 1
}

// invoiceNumbers returns explicit if non-empty, otherwise every invoice
// number found under data/invoices, sorted.
func invoiceNumbers(explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		return explicit, nil
	}
	entries, err := os.ReadDir(invoicesDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", invoicesDir, err)
	}
	var numbers []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		numbers = append(numbers, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(numbers)
	return numbers, nil
}

func renderOne(ctx context.Context, renderer render.Renderer, signer *sign.PDFSigner, number string, force, dryRun bool, manifest render.Manifest) error {
	path := filepath.Join(invoicesDir, number+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var inv invoice.InvoiceJson
	if err := json.Unmarshal(b, &inv); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if inv.Number != number {
		return fmt.Errorf("%s: number field %q does not match filename", path, inv.Number)
	}

	if err := removeStaleDraftPDF(inv, dryRun); err != nil {
		return err
	}

	var totals money.Totals
	var haveTotals bool

	for _, t := range targetsFor(inv) {
		filename := filepath.Base(t.path)

		if !t.overwrite && !force {
			if _, err := os.Stat(t.path); err == nil {
				fmt.Printf("skip  %s (already rendered, use --force to overwrite)\n", t.path)

				if !dryRun {
					totals, haveTotals, err = backfillManifestEntry(&inv, t, filename, manifest, totals, haveTotals)
					if err != nil {
						return err
					}
				}
				continue
			}
		}

		if dryRun {
			fmt.Printf("would render %s -> %s\n", number, t.path)
			continue
		}

		if !haveTotals {
			totals, err = money.Compute(&inv)
			if err != nil {
				return fmt.Errorf("compute totals: %w", err)
			}
			haveTotals = true
		}

		html, err := render.HTML(&inv, totals, t.showPaid)
		if err != nil {
			return fmt.Errorf("render html: %w", err)
		}
		pdf, err := renderer.Render(ctx, html)
		if err != nil {
			return fmt.Errorf("render pdf: %w", err)
		}
		if signer != nil {
			pdf, err = signer.Sign(ctx, pdf)
			if err != nil {
				return fmt.Errorf("sign pdf: %w", err)
			}
		}
		if err := os.MkdirAll(buildDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", buildDir, err)
		}
		if err := os.WriteFile(t.path, pdf, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", t.path, err)
		}
		manifest[filename] = render.HashHTML(html)
		fmt.Printf("wrote %s\n", t.path)
	}
	return nil
}

// backfillManifestEntry records manifest[filename]'s hash from the current
// JSON without touching the existing PDF, when a PDF predates the
// integrity-manifest feature (or the manifest was lost) — so the
// dashboard's staleness check has a baseline to compare future edits
// against. Treats current JSON as authoritative at backfill time — true
// today, since a write-once PDF's JSON hasn't changed since it was made.
// Returns the possibly-just-computed totals/haveTotals so the caller's
// lazy-compute-once bookkeeping carries forward to the rest of the loop.
func backfillManifestEntry(inv *invoice.InvoiceJson, t target, filename string, manifest render.Manifest, totals money.Totals, haveTotals bool) (money.Totals, bool, error) {
	if _, ok := manifest[filename]; ok {
		return totals, haveTotals, nil
	}
	if !haveTotals {
		var err error
		totals, err = money.Compute(inv)
		if err != nil {
			return totals, haveTotals, fmt.Errorf("compute totals: %w", err)
		}
		haveTotals = true
	}
	html, err := render.HTML(inv, totals, t.showPaid)
	if err != nil {
		return totals, haveTotals, fmt.Errorf("backfill manifest for %s: %w", filename, err)
	}
	manifest[filename] = render.HashHTML(html)
	fmt.Printf("backfilled manifest entry for %s\n", filename)
	return totals, haveTotals, nil
}

// removeStaleDraftPDF deletes build/{number}-DRAFT.pdf once an invoice is no
// longer a draft. targetsFor only ever lists targets for an invoice's
// *current* status, so nothing else cleans up the old draft PDF once status
// flips to issued (real example: build/INV-0000000003-DRAFT.pdf, stale since
// the invoice was issued in commit a2c4d03).
func removeStaleDraftPDF(inv invoice.InvoiceJson, dryRun bool) error {
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		return nil
	}
	stale := filepath.Join(buildDir, inv.Number+"-DRAFT.pdf")
	if _, err := os.Stat(stale); err != nil {
		return nil
	}
	if dryRun {
		fmt.Printf("would remove stale %s\n", stale)
		return nil
	}
	if err := os.Remove(stale); err != nil {
		return fmt.Errorf("remove stale %s: %w", stale, err)
	}
	fmt.Printf("removed stale %s\n", stale)
	return nil
}

// target is one PDF artifact to (maybe) render for an invoice.
type target struct {
	path      string
	overwrite bool // true: always re-render. false: write once, skip if it exists (unless --force).
	showPaid  bool
}

// targetsFor returns every PDF artifact inv should have. Drafts get exactly
// one, always overwritten. Issued invoices get the original — written once,
// kept forever, and always rendered as if unpaid regardless of inv.Paid —
// plus a second -paid.pdf, with the paid badge/stamp, once inv.Paid is set.
// See ARCHITECTURE.md §5.1/§6.
func targetsFor(inv invoice.InvoiceJson) []target {
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		return []target{
			{path: filepath.Join(buildDir, inv.Number+"-DRAFT.pdf"), overwrite: true},
		}
	}

	targets := []target{
		{path: filepath.Join(buildDir, inv.Number+".pdf"), overwrite: false, showPaid: false},
	}
	if inv.Paid != nil {
		targets = append(targets, target{
			path: filepath.Join(buildDir, inv.Number+"-paid.pdf"), overwrite: false, showPaid: true,
		})
	}
	return targets
}
