package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/render"
)

// runPrune implements `invoicectl prune [--dry-run]`: removes
// build/*-DRAFT.pdf files whose invoice JSON no longer exists under
// data/invoices — relics from a draft removed by hand (rm/git rm) before
// `invoicectl delete` existed, or any other manual cleanup that skipped it.
// Unlike invoicectl delete, prune never touches data/invoices; it only
// reconciles build/ against what's actually there, so it's safe for CI to
// call unconditionally after every render.
func runPrune(args []string) int {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print what would be removed, without removing it")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	entries, err := os.ReadDir(buildDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("invoicectl prune: nothing to prune")
			return 0
		}
		fmt.Fprintln(os.Stderr, "invoicectl prune:", err)
		return 1
	}

	var manifest render.Manifest
	manifestLoaded := false
	removed := 0

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "-DRAFT.pdf") {
			continue
		}
		number := strings.TrimSuffix(e.Name(), "-DRAFT.pdf")
		jsonPath := filepath.Join(invoicesDir, number+".json")
		if _, err := os.Stat(jsonPath); err == nil {
			continue // still a live draft
		}

		pdfPath := filepath.Join(buildDir, e.Name())
		if *dryRun {
			fmt.Printf("would remove %s (no %s)\n", pdfPath, jsonPath)
			removed++
			continue
		}

		if err := os.Remove(pdfPath); err != nil {
			fmt.Fprintln(os.Stderr, "invoicectl prune:", err)
			return 1
		}
		fmt.Printf("removed %s (no %s)\n", pdfPath, jsonPath)
		removed++

		if !manifestLoaded {
			manifest, err = render.LoadManifest(renderManifestPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, "invoicectl prune:", err)
				return 1
			}
			manifestLoaded = true
		}
		delete(manifest, e.Name())
	}

	if manifestLoaded {
		if err := manifest.Save(renderManifestPath); err != nil {
			fmt.Fprintln(os.Stderr, "invoicectl prune:", err)
			return 1
		}
	}

	if removed == 0 {
		fmt.Println("invoicectl prune: nothing to prune")
	}
	return 0
}
