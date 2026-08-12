package render

import (
	"fmt"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

func validateLocalization(inv *invoice.InvoiceJson) error {
	for i, line := range inv.Lines {
		if _, _, err := line.Description.Require(inv.Language); err != nil {
			return fmt.Errorf("line %d description: %w", i+1, err)
		}
	}
	for i, d := range inv.Discounts {
		if _, _, err := d.Label.Require(inv.Language); err != nil {
			return fmt.Errorf("discount %d label: %w", i+1, err)
		}
	}
	if !inv.Tax.Note.IsEmpty() {
		if _, _, err := inv.Tax.Note.Require(inv.Language); err != nil {
			return fmt.Errorf("tax note: %w", err)
		}
	}
	return nil
}
