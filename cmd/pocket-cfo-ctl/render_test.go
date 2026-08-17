package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// TestSplitFlags covers the regression that motivated it: flag.Parse stops
// at the first non-flag argument, so "render INV-... --force" (flag after
// the invoice number — the exact ordering ARCHITECTURE.md §5.2 and
// README.md document) must still be recognized as --force, not swallowed as
// a second invoice number.
func TestSplitFlags(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantFlags      []string
		wantPositional []string
	}{
		{"empty", nil, nil, nil},
		{"flag only", []string{"--force"}, []string{"--force"}, nil},
		{"number only", []string{"INV-0000000001"}, nil, []string{"INV-0000000001"}},
		{"flag before number", []string{"--force", "INV-0000000001"}, []string{"--force"}, []string{"INV-0000000001"}},
		{"number before flag", []string{"INV-0000000001", "--force"}, []string{"--force"}, []string{"INV-0000000001"}},
		{"number between flags", []string{"--dry-run", "INV-0000000001", "--force"}, []string{"--dry-run", "--force"}, []string{"INV-0000000001"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFlags, gotPositional := splitFlags(tt.args)
			if !reflect.DeepEqual(gotFlags, tt.wantFlags) {
				t.Errorf("flags = %v, want %v", gotFlags, tt.wantFlags)
			}
			if !reflect.DeepEqual(gotPositional, tt.wantPositional) {
				t.Errorf("positional = %v, want %v", gotPositional, tt.wantPositional)
			}
		})
	}
}

func TestSplitFlagsWithValues(t *testing.T) {
	takesValue := map[string]bool{"base-ref": true, "allow-removals": true}
	tests := []struct {
		name           string
		args           []string
		wantFlags      []string
		wantPositional []string
	}{
		{"value flag before the directory", []string{"--base-ref", "HEAD", "data"}, []string{"--base-ref", "HEAD"}, []string{"data"}},
		{"value flag after the directory", []string{"data", "--base-ref", "HEAD"}, []string{"--base-ref", "HEAD"}, []string{"data"}},
		{"equals form keeps its own value", []string{"data", "--base-ref=HEAD"}, []string{"--base-ref=HEAD"}, []string{"data"}},
		{"a value that looks like a directory", []string{"--base-ref", "origin/main", "data"}, []string{"--base-ref", "origin/main"}, []string{"data"}},
		{"two value flags around the directory", []string{"--base-ref", "HEAD", "data", "--allow-removals", "duplicate import"},
			[]string{"--base-ref", "HEAD", "--allow-removals", "duplicate import"}, []string{"data"}},
		{"an unknown flag takes no value", []string{"--force", "data"}, []string{"--force"}, []string{"data"}},
		{"a trailing value flag with nothing after it", []string{"--base-ref"}, []string{"--base-ref"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFlags, gotPositional := splitFlagsWithValues(tt.args, takesValue)
			if !reflect.DeepEqual(gotFlags, tt.wantFlags) {
				t.Errorf("flags = %v, want %v", gotFlags, tt.wantFlags)
			}
			if !reflect.DeepEqual(gotPositional, tt.wantPositional) {
				t.Errorf("positional = %v, want %v", gotPositional, tt.wantPositional)
			}
		})
	}
}

func TestForceAllowed(t *testing.T) {
	tests := []struct {
		name     string
		force    bool
		explicit []string
		want     bool
	}{
		{"no force, no args", false, nil, true},
		{"no force, many args", false, []string{"INV-0000000001", "INV-0000000002"}, true},
		{"force, one invoice", true, []string{"INV-0000000001"}, true},
		{"force, no invoice (bulk)", true, nil, false},
		{"force, many invoices", true, []string{"INV-0000000001", "INV-0000000002"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := forceAllowed(tt.force, tt.explicit); got != tt.want {
				t.Errorf("forceAllowed(%v, %v) = %v, want %v", tt.force, tt.explicit, got, tt.want)
			}
		})
	}
}

func mustDate(s string) types.SerializableDate {
	var d types.SerializableDate
	if err := d.UnmarshalJSON([]byte(`"` + s + `"`)); err != nil {
		panic(err)
	}
	return d
}

// TestTargetsFor pins the artifact set per invoice status/paid combination:
// drafts get exactly one, always-overwritten target; issued invoices get a
// write-once original that never shows paid, plus a -paid.pdf only once a
// payment date is known. That date comes from paid-invoices.json, so it's an
// argument now rather than a field on the invoice.
//
// Expected paths go through filepath.Join, same as targetsFor itself: hardcoded
// forward slashes matched on Linux and failed on Windows, where buildDir joins
// with a backslash.
func TestTargetsFor(t *testing.T) {
	paidDate := mustDate("2026-01-15")
	built := func(name string) string { return filepath.Join(buildDir, name) }

	t.Run("draft", func(t *testing.T) {
		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusDraft}
		got := targetsFor(inv, nil)
		want := []target{{path: built("INV-0000000009-DRAFT.pdf"), overwrite: true}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("targetsFor(draft) = %+v, want %+v", got, want)
		}
	})

	// A draft named in paid-invoices.json is a validation error, not a
	// second artifact — targetsFor must not invent a -paid.pdf for it.
	t.Run("draft, wrongly marked paid", func(t *testing.T) {
		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusDraft}
		got := targetsFor(inv, &paidDate)
		want := []target{{path: built("INV-0000000009-DRAFT.pdf"), overwrite: true}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("targetsFor(draft, paid) = %+v, want %+v", got, want)
		}
	})

	t.Run("issued, unpaid", func(t *testing.T) {
		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusIssued}
		got := targetsFor(inv, nil)
		want := []target{{path: built("INV-0000000009.pdf"), overwrite: false}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("targetsFor(issued, unpaid) = %+v, want %+v", got, want)
		}
	})

	t.Run("issued, paid", func(t *testing.T) {
		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusIssued}
		got := targetsFor(inv, &paidDate)
		want := []target{
			{path: built("INV-0000000009.pdf"), overwrite: false},
			{path: built("INV-0000000009-paid.pdf"), overwrite: false, paidOn: &paidDate,
				staleSources: []string{paidInvoicesPath}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("targetsFor(issued, paid) = %+v, want %+v", got, want)
		}
	})
}

// TestRemoveStaleDraftPDF pins the fix for the real leftover this repo had:
// build/INV-0000000003-DRAFT.pdf survived the invoice being issued because
// targetsFor only ever lists targets for an invoice's *current* status.
func TestRemoveStaleDraftPDF(t *testing.T) {
	t.Run("issued invoice removes a leftover draft PDF", func(t *testing.T) {
		t.Chdir(t.TempDir())
		mustMkdirAll(t, buildDir)
		stale := filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")
		if err := os.WriteFile(stale, []byte("pdf"), 0o644); err != nil {
			t.Fatal(err)
		}

		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusIssued}
		manifest := render.Manifest{"INV-0000000009-DRAFT.pdf": "somehash"}
		if err := removeStaleDraftPDF(inv, manifest, false); err != nil {
			t.Fatalf("removeStaleDraftPDF: %v", err)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("stale draft PDF still present after removeStaleDraftPDF: err = %v", err)
		}
		if _, ok := manifest["INV-0000000009-DRAFT.pdf"]; ok {
			t.Error("the manifest still has an entry for a PDF that no longer exists")
		}
	})

	t.Run("dry-run leaves the file in place", func(t *testing.T) {
		t.Chdir(t.TempDir())
		mustMkdirAll(t, buildDir)
		stale := filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")
		if err := os.WriteFile(stale, []byte("pdf"), 0o644); err != nil {
			t.Fatal(err)
		}

		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusIssued}
		manifest := render.Manifest{}
		if err := removeStaleDraftPDF(inv, manifest, true); err != nil {
			t.Fatalf("removeStaleDraftPDF: %v", err)
		}
		if _, err := os.Stat(stale); err != nil {
			t.Errorf("dry-run should not have removed the file: %v", err)
		}
	})

	t.Run("draft invoice is left alone", func(t *testing.T) {
		t.Chdir(t.TempDir())
		mustMkdirAll(t, buildDir)
		stale := filepath.Join(buildDir, "INV-0000000009-DRAFT.pdf")
		if err := os.WriteFile(stale, []byte("pdf"), 0o644); err != nil {
			t.Fatal(err)
		}

		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusDraft}
		manifest := render.Manifest{}
		if err := removeStaleDraftPDF(inv, manifest, false); err != nil {
			t.Fatalf("removeStaleDraftPDF: %v", err)
		}
		if _, err := os.Stat(stale); err != nil {
			t.Errorf("draft's own -DRAFT.pdf should not be removed: %v", err)
		}
	})

	t.Run("no file present is a no-op, not an error", func(t *testing.T) {
		t.Chdir(t.TempDir())
		inv := invoice.InvoiceJson{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusIssued}
		if err := removeStaleDraftPDF(inv, render.Manifest{}, false); err != nil {
			t.Fatalf("removeStaleDraftPDF: %v", err)
		}
	})
}
