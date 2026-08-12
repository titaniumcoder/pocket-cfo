package invoice

import "fmt"

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

func (ls LocalizedString) IsEmpty() bool {
	return ls.De == nil && ls.En == nil && ls.Fr == nil && ls.Bg == nil
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
