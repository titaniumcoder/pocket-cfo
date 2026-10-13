package render

import (
	"path/filepath"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// pdfDir is where the artifact under judgement lives — <build>/. For issued
// invoices the PDF is read and hashed — the entry in the manifest was taken
// from the PDF's own bytes at render time, so a template change leaves the
// pair untouched and the invoice stays current. Drafts re-render on every
// run, so their entry is an HTML hash and the comparison re-derives that HTML.
func IsCurrent(inv *invoice.InvoiceJson, totals money.Totals, manifest Manifest, pdfDir string) bool {
	file := inv.Number + ".pdf"
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		file = inv.Number + "-DRAFT.pdf"
	}

	want, ok := manifest[file]
	if !ok {
		return false
	}
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		html, err := HTML(inv, totals, nil)
		if err != nil {
			return false
		}
		return want.HTMLHash != "" && HashHTML(html) == want.HTMLHash
	}
	got, err := HashPDFFile(filepath.Join(pdfDir, file))
	if err != nil {
		return false
	}
	return want.PDFHash != "" && got == want.PDFHash
}
