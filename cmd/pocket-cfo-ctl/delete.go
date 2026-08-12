package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

func runDelete(args []string) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print what would be removed, without removing it")

	flagArgs, positional := splitFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positional) != 1 {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl delete: exactly one invoice number is required")
		return 1
	}
	number := positional[0]

	jsonPath := filepath.Join(invoicesDir, number+".json")
	b, err := os.ReadFile(jsonPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl delete:", err)
		return 1
	}
	var inv invoice.InvoiceJson
	if err := json.Unmarshal(b, &inv); err != nil {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl delete: parse %s: %v\n", jsonPath, err)
		return 1
	}
	if inv.Number != number {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl delete: %s: number field %q does not match filename\n", jsonPath, inv.Number)
		return 1
	}
	if inv.Status != invoice.InvoiceJsonStatusDraft {
		fmt.Fprintf(os.Stderr, "pocket-cfo-ctl delete: %s is %s, not draft — an issued invoice is kept, never deleted (ARCHITECTURE.md §3.7)\n", number, inv.Status)
		return 1
	}

	pdfPath := filepath.Join(buildDir, number+"-DRAFT.pdf")
	pdfFilename := filepath.Base(pdfPath)
	_, pdfErr := os.Stat(pdfPath)
	hasPDF := pdfErr == nil

	if *dryRun {
		fmt.Printf("would remove %s\n", jsonPath)
		if hasPDF {
			fmt.Printf("would remove %s\n", pdfPath)
		}
		return 0
	}

	if err := os.Remove(jsonPath); err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl delete:", err)
		return 1
	}
	fmt.Printf("removed %s\n", jsonPath)

	if hasPDF {
		if err := os.Remove(pdfPath); err != nil {
			fmt.Fprintln(os.Stderr, "pocket-cfo-ctl delete:", err)
			return 1
		}
		fmt.Printf("removed %s\n", pdfPath)
	}

	manifest, err := render.LoadManifest(renderManifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-cfo-ctl delete:", err)
		return 1
	}
	if _, ok := manifest[pdfFilename]; ok {
		delete(manifest, pdfFilename)
		if err := manifest.Save(renderManifestPath); err != nil {
			fmt.Fprintln(os.Stderr, "pocket-cfo-ctl delete:", err)
			return 1
		}
		fmt.Printf("removed manifest entry for %s\n", pdfFilename)
	}

	return 0
}
