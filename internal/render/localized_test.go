package render

import (
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// TestValidateLocalization_MissingTranslation covers the fail-loudly path:
// an "en"-language invoice missing its own "en" description key must be
// rejected, not silently rendered with blank text on a legal document.
func TestValidateLocalization_MissingTranslation(t *testing.T) {
	inv := &invoice.InvoiceJson{
		Language: invoice.InvoiceJsonLanguageEn,
		Lines: []invoice.Line{
			{Description: invoice.LocalizedString{De: strp("Hallo"), Bg: strp("Здравей")}},
		},
		Tax: invoice.Tax{Note: invoice.LocalizedString{De: strp("Hinweis"), Bg: strp("Бележка")}},
	}
	if err := validateLocalization(inv); err == nil {
		t.Fatal("expected an error for a missing \"en\" description key, got nil")
	}
}

func TestValidateLocalization_Complete(t *testing.T) {
	inv := &invoice.InvoiceJson{
		Language: invoice.InvoiceJsonLanguageEn,
		Lines: []invoice.Line{
			{Description: invoice.LocalizedString{En: strp("Hello"), Bg: strp("Здравей")}},
		},
		Discounts: []invoice.Discount{
			{Label: invoice.LocalizedString{En: strp("Discount"), Bg: strp("Отстъпка")}, Percent: intp(100)},
		},
		Tax: invoice.Tax{Note: invoice.LocalizedString{En: strp("Note"), Bg: strp("Бележка")}},
	}
	if err := validateLocalization(inv); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func intp(v int) *int { return &v }

// TestValidateLocalization_EmptyTaxNote covers the "no note needed" state
// (e.g. a domestic invoice with nothing special to explain) — an entirely
// empty tax note must pass validation, not be treated as a missing
// translation.
func TestValidateLocalization_EmptyTaxNote(t *testing.T) {
	inv := &invoice.InvoiceJson{
		Language: invoice.InvoiceJsonLanguageBg,
		Lines: []invoice.Line{
			{Description: invoice.LocalizedString{Bg: strp("Работа")}},
		},
		Tax: invoice.Tax{Note: invoice.LocalizedString{}},
	}
	if err := validateLocalization(inv); err != nil {
		t.Fatalf("expected an entirely empty tax note to pass, got %v", err)
	}
}

// TestValidateLocalization_PartialTaxNoteStillFails covers the case an
// empty-note carve-out must NOT swallow: a tax note with *some* content but
// missing the required translation is still a real gap.
func TestValidateLocalization_PartialTaxNoteStillFails(t *testing.T) {
	inv := &invoice.InvoiceJson{
		Language: invoice.InvoiceJsonLanguageDe,
		Lines: []invoice.Line{
			{Description: invoice.LocalizedString{De: strp("Arbeit"), Bg: strp("Работа")}},
		},
		Tax: invoice.Tax{Note: invoice.LocalizedString{De: strp("Hinweis")}}, // missing bg
	}
	if err := validateLocalization(inv); err == nil {
		t.Fatal("expected a partially-filled tax note (missing bg) to fail, got nil")
	}
}
