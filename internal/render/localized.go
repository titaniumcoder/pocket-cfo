package render

import "github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"

func validateLocalization(inv *invoice.InvoiceJson) error {
	return invoice.ValidateLocalization(inv)
}
