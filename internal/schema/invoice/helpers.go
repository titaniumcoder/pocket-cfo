package invoice

import "fmt"

// Get returns ls's text for lang and whether it was present at all. Every
// key on LocalizedString is individually optional at the JSON Schema level
// (see schemas/invoice.json) — Require, not the schema, is what enforces
// which keys must actually be present for a given invoice.
func (ls LocalizedString) Get(lang InvoiceJsonLanguage) (string, bool) {
	var p *string
	switch lang {
	case InvoiceJsonLanguageDe:
		p = ls.De
	case InvoiceJsonLanguageEn:
		p = ls.En
	case InvoiceJsonLanguageFr:
		p = ls.Fr
	case InvoiceJsonLanguageBg:
		p = ls.Bg
	}
	if p == nil {
		return "", false
	}
	return *p, true
}

// IsEmpty reports whether ls has no text in any language at all — the
// deliberate "nothing to say here" state, distinct from a partially-filled
// LocalizedString that Require should still reject. Used by the tax note,
// the one field allowed to be entirely absent (e.g. a domestic invoice that
// needs no explanatory wording).
func (ls LocalizedString) IsEmpty() bool {
	return ls.De == nil && ls.En == nil && ls.Fr == nil && ls.Bg == nil
}

// Require enforces the "translation complete" business rule (see
// ARCHITECTURE.md §4.3): lang's own key must be present, and bg must be
// present too unless lang IS bg — bg is the one translation that's always
// legally required. Fails loudly rather than let a caller render blank text
// on a legal document.
func (ls LocalizedString) Require(lang InvoiceJsonLanguage) (primary, secondary string, err error) {
	primary, ok := ls.Get(lang)
	if !ok {
		return "", "", fmt.Errorf("missing %q text", lang)
	}
	if lang == InvoiceJsonLanguageBg {
		return primary, "", nil
	}
	secondary, ok = ls.Get(InvoiceJsonLanguageBg)
	if !ok {
		return "", "", fmt.Errorf("missing required \"bg\" translation")
	}
	return primary, secondary, nil
}
