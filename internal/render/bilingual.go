package render

import "github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"

// inlineThreshold is the deterministic char-length heuristic deciding
// whether a primary+bg pair fits on one line at the same font size. It's
// computed in Go, not measured client-side in the rendered PDF — this
// pipeline is already timing-fragile around font loading (see
// ARCHITECTURE.md §6), and a fixed heuristic keeps HTML rendering
// deterministic for the golden-test strategy (ARCHITECTURE.md §9).
const inlineThreshold = 60

// Bilingual is a primary-language string plus its Bulgarian translation,
// with a decision on whether the two fit on the same line.
type Bilingual struct {
	Primary   string
	Secondary string // "" when lang is bg (nothing to translate to)
	Inline    bool
}

// bilingual builds the primary/secondary pair for a template field. It
// assumes validateLocalization has already verified the required keys are
// present (see localized.go) — a missing key here just renders as empty
// rather than panicking, since render.HTML refuses to run at all if
// validation fails first.
func bilingual(ls invoice.LocalizedString, lang invoice.InvoiceJsonLanguage) Bilingual {
	primary, _ := ls.Get(lang)
	if lang == invoice.InvoiceJsonLanguageBg {
		return Bilingual{Primary: primary}
	}
	secondary, _ := ls.Get(invoice.InvoiceJsonLanguageBg)
	if secondary == "" {
		return Bilingual{Primary: primary}
	}
	return Bilingual{
		Primary:   primary,
		Secondary: secondary,
		Inline:    len(primary)+len(secondary) <= inlineThreshold,
	}
}
