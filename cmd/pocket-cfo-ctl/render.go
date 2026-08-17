package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
)

var (
	dataDir            = getenv("DATA_DIR", "data")
	invoicesDir        = dataDir + "/invoices"
	paidInvoicesPath   = dataDir + "/paid-invoices.json"
	buildDir           = getenv("BUILD_DIR", "build")
	renderManifestPath = buildDir + "/render-manifest.json"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func runRender(args []string) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	force := fs.Bool("force", false, "re-render even an already-issued PDF (requires a single invoice number)")
	dryRun := fs.Bool("dry-run", false, "print what would render, without rendering")

	flagArgs, explicit := splitFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		return parseExit(err)
	}
	if !forceAllowed(*force, explicit) {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl render: --force requires exactly one invoice number (bulk force-render is not allowed)")
		return 1
	}

	numbers, err := invoiceNumbers(explicit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl render:", err)
		return 1
	}
	if len(numbers) == 0 {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl render: no invoices found under", invoicesDir)
		return 1
	}

	var renderer render.Renderer
	if !*dryRun {
		apiKey := os.Getenv("API2PDF_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "pocket-cfo-ctl render: API2PDF_KEY is not set (see .envrc.example)")
			return 1
		}
		renderer = render.NewAPI2PDF(apiKey)
	}

	manifest := render.Manifest{}
	if !*dryRun {
		var err error
		manifest, err = render.LoadManifest(renderManifestPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pocket-cfo-ctl render:", err)
			return 1
		}
	}

	paid, err := stats.LoadPaid(paidInvoicesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl render:", err)
		return 1
	}

	failed := 0
	for _, number := range numbers {
		var paidOn *types.SerializableDate
		if d, ok := paid[number]; ok {
			paidOn = &d
		}
		if err := renderOne(context.Background(), renderer, number, *force, *dryRun, manifest, paidOn); err != nil {
			fmt.Fprintf(os.Stderr, "pocket-cfo-ctl render: %s: %v\n", number, err)
			failed++
		}
	}

	if !*dryRun {
		if err := manifest.Save(renderManifestPath); err != nil {
			fmt.Fprintln(os.Stderr, "pocket-cfo-ctl render:", err)
			return 1
		}
	}

	if failed > 0 {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl render: %d of %d invoice(s) failed\n", failed, len(numbers))
		return 1
	}
	return 0
}

func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func splitFlags(args []string) (flagArgs, positional []string) {
	return splitFlagsWithValues(args, nil)
}

func splitFlagsWithValues(args []string, takesValue map[string]bool) (flagArgs, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		if strings.Contains(a, "=") {
			continue
		}
		if takesValue[strings.TrimLeft(a, "-")] && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return flagArgs, positional
}

func forceAllowed(force bool, explicit []string) bool {
	return !force || len(explicit) == 1
}

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

func renderOne(ctx context.Context, renderer render.Renderer, number string, force, dryRun bool, manifest render.Manifest, paidOn *types.SerializableDate) error {
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

	if err := removeStaleDraftPDF(inv, manifest, dryRun); err != nil {
		return err
	}

	var totals money.Totals
	var haveTotals bool

	for _, t := range targetsFor(inv, paidOn) {
		filename := filepath.Base(t.path)

		if !t.overwrite && !force {
			if _, err := os.Stat(t.path); err == nil {
				stale, serr := t.staleAgainst(path)
				if serr != nil {
					return serr
				}
				if !stale {
					fmt.Printf("skip  %s (already rendered, use --force to overwrite)\n", t.path)

					if !dryRun {
						totals, haveTotals, err = backfillManifestEntry(&inv, t, filename, manifest, totals, haveTotals)
						if err != nil {
							return err
						}
					}
					continue
				}
				fmt.Printf("stale %s (its source changed since it was written)\n", t.path)
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

		html, err := render.HTML(&inv, totals, t.paidOn)
		if err != nil {
			return fmt.Errorf("render html: %w", err)
		}
		pdf, err := renderer.Render(ctx, html)
		if err != nil {
			return fmt.Errorf("render pdf: %w", err)
		}
		if err := os.MkdirAll(buildDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", buildDir, err)
		}
		if err := writeAtomic(t.path, pdf); err != nil {
			return err
		}
		manifest[filename] = render.HashHTML(html)
		fmt.Printf("wrote %s\n", t.path)
	}
	return nil
}

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
	html, err := render.HTML(inv, totals, t.paidOn)
	if err != nil {
		return totals, haveTotals, fmt.Errorf("backfill manifest for %s: %w", filename, err)
	}
	manifest[filename] = render.HashHTML(html)
	fmt.Printf("backfilled manifest entry for %s\n", filename)
	return totals, haveTotals, nil
}

func removeStaleDraftPDF(inv invoice.InvoiceJson, manifest render.Manifest, dryRun bool) error {
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
	delete(manifest, filepath.Base(stale))
	fmt.Printf("removed stale %s\n", stale)
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

type target struct {
	path         string
	overwrite    bool
	paidOn       *types.SerializableDate
	staleSources []string
}

func (t target) staleAgainst(invoicePath string) (bool, error) {
	if len(t.staleSources) == 0 {
		return false, nil
	}
	clock, err := newGitClock()
	if err != nil {
		return false, err
	}
	sources := append([]string{invoicePath}, t.staleSources...)
	return clock.newer(t.path, sources...)
}

func targetsFor(inv invoice.InvoiceJson, paidOn *types.SerializableDate) []target {
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		return []target{
			{path: filepath.Join(buildDir, inv.Number+"-DRAFT.pdf"), overwrite: true},
		}
	}

	targets := []target{
		{path: filepath.Join(buildDir, inv.Number+".pdf"), overwrite: false},
	}
	if paidOn != nil {
		targets = append(targets, target{
			path: filepath.Join(buildDir, inv.Number+"-paid.pdf"), overwrite: false, paidOn: paidOn,
			staleSources: []string{paidInvoicesPath},
		})
	}
	return targets
}
