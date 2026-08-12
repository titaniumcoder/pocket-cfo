package render

import (
	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

func IsCurrent(inv *invoice.InvoiceJson, totals money.Totals, manifest Manifest) bool {
	file := inv.Number + ".pdf"
	if inv.Status == invoice.InvoiceJsonStatusDraft {
		file = inv.Number + "-DRAFT.pdf"
	}

	want, ok := manifest[file]
	if !ok {
		return false
	}
	html, err := HTML(inv, totals, nil)
	if err != nil {
		return false
	}
	return HashHTML(html) == want
}
