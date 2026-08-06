package render

import (
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

func TestCombinedLabels_Bulgarian(t *testing.T) {
	got := CombinedLabels(invoice.InvoiceJsonLanguageBg)
	if got != labelsBG {
		t.Errorf("CombinedLabels(bg) = %+v, want labelsBG unchanged (%+v)", got, labelsBG)
	}
}

func TestCombinedLabels_NonBulgarian(t *testing.T) {
	cases := []struct {
		lang invoice.InvoiceJsonLanguage
		want Labels
	}{
		{invoice.InvoiceJsonLanguageDe, labelsDE},
		{invoice.InvoiceJsonLanguageEn, labelsEN},
		{invoice.InvoiceJsonLanguageFr, labelsFR},
	}
	for _, c := range cases {
		t.Run(string(c.lang), func(t *testing.T) {
			got := CombinedLabels(c.lang)

			checks := map[string]struct{ got, wantPrimary, wantBg string }{
				"DocTitle":   {got.DocTitle, c.want.DocTitle, labelsBG.DocTitle},
				"BillTo":     {got.BillTo, c.want.BillTo, labelsBG.BillTo},
				"PaidBadge":  {got.PaidBadge, c.want.PaidBadge, labelsBG.PaidBadge},
				"AmountDue":  {got.AmountDue, c.want.AmountDue, labelsBG.AmountDue},
				"TaxIdLabel": {got.TaxIdLabel, c.want.TaxIdLabel, labelsBG.TaxIdLabel},
				"VatIdLabel": {got.VatIdLabel, c.want.VatIdLabel, labelsBG.VatIdLabel},
			}
			for field, c := range checks {
				want := c.wantPrimary + " / " + c.wantBg
				if c.got != want {
					t.Errorf("%s = %q, want %q", field, c.got, want)
				}
			}
		})
	}
}
