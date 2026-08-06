package render

import (
	"fmt"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// validateLocalization enforces the "translation complete" business rule
// (ARCHITECTURE.md §4.3) at render time: every localized field must have
// inv's own language plus a Bulgarian translation (unless inv.Language IS
// bg). This is deliberately not a JSON Schema constraint — see
// invoice.LocalizedString.Require — so it must run before HTML() executes
// the template, or a missing translation would render as blank text on a
// legal document instead of failing loudly.
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
	// An entirely empty tax note is a deliberate "nothing to say here" state
	// (e.g. a domestic invoice needing no explanatory wording) — only a
	// partially-filled one (some language present, but not the required
	// pair) is a real translation gap that must still fail loudly.
	if !inv.Tax.Note.IsEmpty() {
		if _, _, err := inv.Tax.Note.Require(inv.Language); err != nil {
			return fmt.Errorf("tax note: %w", err)
		}
	}
	return nil
}
