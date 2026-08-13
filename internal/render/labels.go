package render

import "github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"

type Labels struct {
	DocTitle        string
	PaidBadge       string
	BillTo          string
	InvoiceDate     string
	DueDate         string
	Currency        string
	AmountDue       string
	DescriptionCol  string
	QtyCol          string
	UnitPriceCol    string
	VatCol          string
	AmountCol       string
	Subtotal        string
	Net             string
	VatTotal        string
	GrandTotal      string
	NotesTitle      string
	PaymentTitle    string
	AccountHolder   string
	Bank            string
	Iban            string
	Bic             string
	IntermediaryBic string
	Reference       string
	TaxIdLabel      string
	VatIdLabel      string
}

var labelsDE = Labels{
	DocTitle:        "RECHNUNG",
	PaidBadge:       "BEZAHLT",
	BillTo:          "RECHNUNGSEMPFÄNGER",
	InvoiceDate:     "Rechnungsdatum",
	DueDate:         "Fällig am",
	Currency:        "Währung",
	AmountDue:       "Fälliger Betrag",
	DescriptionCol:  "Beschreibung",
	QtyCol:          "Menge",
	UnitPriceCol:    "Einzelpreis",
	VatCol:          "MwSt.",
	AmountCol:       "Betrag",
	Subtotal:        "Zwischensumme",
	Net:             "Nettobetrag",
	VatTotal:        "MwSt. gesamt",
	GrandTotal:      "Gesamtbetrag",
	NotesTitle:      "Rechnungshinweis",
	PaymentTitle:    "Zahlungsinformationen",
	AccountHolder:   "Kontoinhaber",
	Bank:            "Bank",
	Iban:            "IBAN",
	Bic:             "BIC",
	IntermediaryBic: "Intermediär-BIC",
	Reference:       "Verwendungszweck",
	TaxIdLabel:      "UID",
	VatIdLabel:      "USt-IdNr. / MWST-Nr.",
}

var labelsBG = Labels{
	DocTitle:        "ФАКТУРА",
	PaidBadge:       "ПЛАТЕНО",
	BillTo:          "ПОЛУЧАТЕЛ",
	InvoiceDate:     "Дата на фактура",
	DueDate:         "Падеж",
	Currency:        "Валута",
	AmountDue:       "Дължима сума",
	DescriptionCol:  "Описание",
	QtyCol:          "Количество",
	UnitPriceCol:    "Единична цена",
	VatCol:          "ДДС",
	AmountCol:       "Сума",
	Subtotal:        "Междинна сума",
	Net:             "Нетна сума",
	VatTotal:        "ДДС общо",
	GrandTotal:      "Обща сума",
	NotesTitle:      "Забележка към фактурата",
	PaymentTitle:    "Информация за плащане",
	AccountHolder:   "Титуляр на сметка",
	Bank:            "Банка",
	Iban:            "IBAN",
	Bic:             "BIC",
	IntermediaryBic: "Интермедиерен BIC",
	Reference:       "Основание за плащане",
	TaxIdLabel:      "ЕИК",
	VatIdLabel:      "ДДС №",
}

var labelsEN = Labels{
	DocTitle:        "INVOICE",
	PaidBadge:       "PAID",
	BillTo:          "BILL TO",
	InvoiceDate:     "Invoice date",
	DueDate:         "Due date",
	Currency:        "Currency",
	AmountDue:       "Amount due",
	DescriptionCol:  "Description",
	QtyCol:          "Qty",
	UnitPriceCol:    "Unit price",
	VatCol:          "VAT",
	AmountCol:       "Amount",
	Subtotal:        "Subtotal",
	Net:             "Net amount",
	VatTotal:        "VAT total",
	GrandTotal:      "Total amount",
	NotesTitle:      "Invoice note",
	PaymentTitle:    "Payment information",
	AccountHolder:   "Account holder",
	Bank:            "Bank",
	Iban:            "IBAN",
	Bic:             "BIC",
	IntermediaryBic: "Intermediary BIC",
	Reference:       "Payment reference",
	TaxIdLabel:      "UIC",
	VatIdLabel:      "VAT",
}

var labelsFR = Labels{
	DocTitle:        "FACTURE",
	PaidBadge:       "PAYÉE",
	BillTo:          "FACTURÉ À",
	InvoiceDate:     "Date de facture",
	DueDate:         "Date d'échéance",
	Currency:        "Devise",
	AmountDue:       "Montant dû",
	DescriptionCol:  "Description",
	QtyCol:          "Qté",
	UnitPriceCol:    "Prix unitaire",
	VatCol:          "TVA",
	AmountCol:       "Montant",
	Subtotal:        "Sous-total",
	Net:             "Montant net",
	VatTotal:        "TVA totale",
	GrandTotal:      "Montant total",
	NotesTitle:      "Remarque sur la facture",
	PaymentTitle:    "Informations de paiement",
	AccountHolder:   "Titulaire du compte",
	Bank:            "Banque",
	Iban:            "IBAN",
	Bic:             "BIC",
	IntermediaryBic: "BIC intermédiaire",
	Reference:       "Référence de paiement",
	TaxIdLabel:      "CUI",
	VatIdLabel:      "N° TVA",
}

func LabelsFor(lang invoice.InvoiceJsonLanguage) Labels {
	switch lang {
	case invoice.InvoiceJsonLanguageBg:
		return labelsBG
	case invoice.InvoiceJsonLanguageEn:
		return labelsEN
	case invoice.InvoiceJsonLanguageFr:
		return labelsFR
	default:
		return labelsDE
	}
}

func CombinedLabels(lang invoice.InvoiceJsonLanguage) Labels {
	if lang == invoice.InvoiceJsonLanguageBg {
		return labelsBG
	}
	p := LabelsFor(lang)
	b := labelsBG
	return Labels{
		DocTitle:        p.DocTitle + " / " + b.DocTitle,
		PaidBadge:       p.PaidBadge + " / " + b.PaidBadge,
		BillTo:          p.BillTo + " / " + b.BillTo,
		InvoiceDate:     p.InvoiceDate + " / " + b.InvoiceDate,
		DueDate:         p.DueDate + " / " + b.DueDate,
		Currency:        p.Currency + " / " + b.Currency,
		AmountDue:       p.AmountDue + " / " + b.AmountDue,
		DescriptionCol:  p.DescriptionCol + " / " + b.DescriptionCol,
		QtyCol:          p.QtyCol + " / " + b.QtyCol,
		UnitPriceCol:    p.UnitPriceCol + " / " + b.UnitPriceCol,
		VatCol:          p.VatCol + " / " + b.VatCol,
		AmountCol:       p.AmountCol + " / " + b.AmountCol,
		Subtotal:        p.Subtotal + " / " + b.Subtotal,
		Net:             p.Net + " / " + b.Net,
		VatTotal:        p.VatTotal + " / " + b.VatTotal,
		GrandTotal:      p.GrandTotal + " / " + b.GrandTotal,
		NotesTitle:      p.NotesTitle + " / " + b.NotesTitle,
		PaymentTitle:    p.PaymentTitle + " / " + b.PaymentTitle,
		AccountHolder:   p.AccountHolder + " / " + b.AccountHolder,
		Bank:            p.Bank + " / " + b.Bank,
		Iban:            p.Iban + " / " + b.Iban,
		Bic:             p.Bic + " / " + b.Bic,
		IntermediaryBic: p.IntermediaryBic + " / " + b.IntermediaryBic,
		Reference:       p.Reference + " / " + b.Reference,
		TaxIdLabel:      p.TaxIdLabel + " / " + b.TaxIdLabel,
		VatIdLabel:      p.VatIdLabel + " / " + b.VatIdLabel,
	}
}
