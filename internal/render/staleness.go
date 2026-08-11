package render

import (
	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// IsCurrent reports whether the PDF already built for inv still reflects its
// current JSON content, using manifest as the precomputed reference — see
// manifest.go and ARCHITECTURE.md §5.1's "Staleness" rules, which this
// extends into something checkable at request time rather than only at
// build time.
//
// This deliberately hashes rendered HTML, not raw JSON: the archived PDF is
// always rendered as-if-unpaid, so a field the template never shows can't
// report as drift.
//
// It answers exactly one question — does the archived original still match
// its JSON — and deliberately ignores whether INV-…-paid.pdf has been built.
// That artifact's absence is a "not rendered yet" fact, not drift, and
// checking it here made marking an invoice paid redden the row until the
// next render run.
//
// A render.HTML error (e.g. a genuinely broken draft mid-edit) is reported
// as "not current" rather than propagated — one bad invoice must never take
// down the whole dashboard.
func IsCurrent(inv *invoice.InvoiceJson, totals money.Totals, manifest Manifest) bool {
	file := inv.Number + ".pdf"
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		file = inv.Number + "-DRAFT.pdf"
	}

	want, ok := manifest[file]
	if !ok {
		return false
	}
	html, err := HTML(inv, totals, false)
	if err != nil {
		return false
	}
	return HashHTML(html) == want
}
