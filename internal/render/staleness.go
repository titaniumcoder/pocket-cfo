package render

import (
	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// IsCurrent reports whether the PDF(s) already built for inv still reflect
// its current JSON content, using manifest as the precomputed reference —
// see manifest.go and ARCHITECTURE.md §5.1's "Staleness" rules, which this
// extends into something checkable at request time rather than only at
// build time.
//
// This deliberately hashes rendered HTML, not raw JSON: the plain/DRAFT PDF
// is always rendered as-if-unpaid (showPaid=false) regardless of inv.Paid's
// actual value, so a raw-JSON hash would flag it stale the instant `paid`
// is set even though that PDF's real content never touches the field.
//
// A render.HTML error (e.g. a genuinely broken draft mid-edit) is reported
// as "not current" rather than propagated — one bad invoice must never take
// down the whole dashboard.
func IsCurrent(inv *invoice.InvoiceJson, totals money.Totals, manifest Manifest) bool {
	mainFile := inv.Number + ".pdf"
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		mainFile = inv.Number + "-DRAFT.pdf"
	}
	if !matches(inv, totals, false, mainFile, manifest) {
		return false
	}
	if inv.Paid != nil {
		if !matches(inv, totals, true, inv.Number+"-paid.pdf", manifest) {
			return false
		}
	}
	return true
}

func matches(inv *invoice.InvoiceJson, totals money.Totals, showPaid bool, file string, manifest Manifest) bool {
	want, ok := manifest[file]
	if !ok {
		return false
	}
	html, err := HTML(inv, totals, showPaid)
	if err != nil {
		return false
	}
	return HashHTML(html) == want
}
