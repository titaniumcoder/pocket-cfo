package render

import "github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"

const inlineThreshold = 60

type Bilingual struct {
	Primary   string
	Secondary string
	Inline    bool
}

func bilingual(ls invoice.LocalizedString, lang invoice.InvoiceJsonLanguage) Bilingual {
	texts := ls.RenderedTexts(lang)
	switch len(texts) {
	case 0:
		return Bilingual{}
	case 1:
		return Bilingual{Primary: texts[0]}
	}
	primary, secondary := texts[0], texts[1]
	return Bilingual{
		Primary:   primary,
		Secondary: secondary,
		Inline:    len([]rune(primary))+len([]rune(secondary)) <= inlineThreshold,
	}
}
