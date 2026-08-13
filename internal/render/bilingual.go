package render

import "github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"

const inlineThreshold = 60

type Bilingual struct {
	Primary   string
	Secondary string
	Inline    bool
}

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
