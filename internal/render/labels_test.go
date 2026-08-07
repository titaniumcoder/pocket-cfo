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
	tests := []struct {
		name string
		lang invoice.InvoiceJsonLanguage
		want Labels
	}{
		{"German", invoice.InvoiceJsonLanguageDe, labelsDE},
		{"English", invoice.InvoiceJsonLanguageEn, labelsEN},
		{"French", invoice.InvoiceJsonLanguageFr, labelsFR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CombinedLabels(tt.lang)

			checks := map[string]struct{ got, wantPrimary, wantBg string }{
				"DocTitle":   {got.DocTitle, tt.want.DocTitle, labelsBG.DocTitle},
				"BillTo":     {got.BillTo, tt.want.BillTo, labelsBG.BillTo},
				"PaidBadge":  {got.PaidBadge, tt.want.PaidBadge, labelsBG.PaidBadge},
				"AmountDue":  {got.AmountDue, tt.want.AmountDue, labelsBG.AmountDue},
				"TaxIdLabel": {got.TaxIdLabel, tt.want.TaxIdLabel, labelsBG.TaxIdLabel},
				"VatIdLabel": {got.VatIdLabel, tt.want.VatIdLabel, labelsBG.VatIdLabel},
			}
			for field, chk := range checks {
				want := chk.wantPrimary + " / " + chk.wantBg
				if chk.got != want {
					t.Errorf("%s = %q, want %q", field, chk.got, want)
				}
			}
		})
	}
}
