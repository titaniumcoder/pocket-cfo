package invoice

import (
	"errors"
	"fmt"
)

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

func TaxRegimes() []TaxRegime {
	out := make([]TaxRegime, 0, len(enumValues_TaxRegime))
	for _, v := range enumValues_TaxRegime {
		out = append(out, TaxRegime(v.(string)))
	}
	return out
}

func (ls LocalizedString) IsEmpty() bool {
	return ls.De == nil && ls.En == nil && ls.Fr == nil && ls.Bg == nil
}

func (ls LocalizedString) RenderedTexts(lang InvoiceJsonLanguage) []string {
	primary, _ := ls.Get(lang)
	if lang == InvoiceJsonLanguageBg {
		if primary == "" {
			return nil
		}
		return []string{primary}
	}
	secondary, _ := ls.Get(InvoiceJsonLanguageBg)

	var out []string
	for _, s := range []string{primary, secondary} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func ValidateLocalization(inv *InvoiceJson) error {
	var problems []error
	for i, line := range inv.Lines {
		if _, _, err := line.Description.Require(inv.Language); err != nil {
			problems = append(problems, fmt.Errorf("line %d description: %w", i+1, err))
		}
	}
	for i, d := range inv.Discounts {
		if _, _, err := d.Label.Require(inv.Language); err != nil {
			problems = append(problems, fmt.Errorf("discount %d label: %w", i+1, err))
		}
	}
	if !inv.Tax.Note.IsEmpty() {
		if _, _, err := inv.Tax.Note.Require(inv.Language); err != nil {
			problems = append(problems, fmt.Errorf("tax note: %w", err))
		}
	}
	return errors.Join(problems...)
}

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
