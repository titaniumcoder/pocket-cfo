package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// chdirRepoRoot points the test at the repo root, like render_test.go's
// fixture-loading tests — render.HTML reads web/templates/invoice.html.tmpl
// relative to the repo root.
func chdirRepoRoot(t *testing.T) {
	t.Helper()
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
}

func stalenessFixture(number string, status invoice.InvoiceJsonStatus) *invoice.InvoiceJson {
	return &invoice.InvoiceJson{
		SchemaVersion: 1, Number: number, Status: status,
		Type: "invoice", Title: "Test", IssueDate: mustDate("2026-01-01"), DueDate: mustDate("2026-01-15"),
		Currency: "EUR", Language: invoice.InvoiceJsonLanguageDe,
		Issuer: invoice.IssuerSnapshot{
			LegalName: "Issuer", Address: invoice.Address{Line1: "S1", PostalCode: "1000", City: "Sofia", CountryCode: "BG"},
			TaxId: "1", VatId: "BG1", Bank: invoice.Bank{Name: "B", Iban: "I", Bic: "B"}, DefaultCurrency: "EUR",
		},
		Recipient: invoice.RecipientSnapshot{
			Number: 1, LegalName: "R", IsBusiness: true, Language: invoice.RecipientSnapshotLanguageDe,
			PaymentTermsDays: 14, Email: "r@example.com",
			Address: invoice.Address{Line1: "S2", PostalCode: "2000", City: "Vienna", CountryCode: "AT"},
		},
		Lines: []invoice.Line{
			{Description: invoice.LocalizedString{De: strp("Arbeit"), Bg: strp("Работа")}, UnitPrice: 10000, VatRate: 0},
		},
		Tax: invoice.Tax{
			Regime: invoice.TaxRegimeOutsideEuPlaceOfSupply, Citations: []string{"c"},
			Note: invoice.LocalizedString{De: strp("Hinweis"), Bg: strp("Бележка")},
		},
	}
}

func mustDate(s string) types.SerializableDate {
	var d types.SerializableDate
	if err := d.UnmarshalJSON([]byte(`"` + s + `"`)); err != nil {
		panic(err)
	}
	return d
}

func TestIsCurrent(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) (*invoice.InvoiceJson, money.Totals, Manifest)
		wantCurrent bool
	}{
		{
			name: "matches",
			setup: func(t *testing.T) (*invoice.InvoiceJson, money.Totals, Manifest) {
				inv := stalenessFixture("INV-0000000001", invoice.InvoiceJsonStatusIssued)
				totals, err := money.Compute(inv)
				if err != nil {
					t.Fatal(err)
				}
				html, err := HTML(inv, totals, false)
				if err != nil {
					t.Fatal(err)
				}
				return inv, totals, Manifest{"INV-0000000001.pdf": HashHTML(html)}
			},
			wantCurrent: true,
		},
		{
			name: "JSON changed",
			setup: func(t *testing.T) (*invoice.InvoiceJson, money.Totals, Manifest) {
				inv := stalenessFixture("INV-0000000001", invoice.InvoiceJsonStatusIssued)
				totals, err := money.Compute(inv)
				if err != nil {
					t.Fatal(err)
				}
				html, err := HTML(inv, totals, false)
				if err != nil {
					t.Fatal(err)
				}
				manifest := Manifest{"INV-0000000001.pdf": HashHTML(html)}

				// Change a field that's actually rendered (the line
				// description, not e.g. Title, which the template doesn't
				// even show), then recompute totals — money.Compute
				// snapshots Description into Totals.Lines, so a real caller
				// (like cmd/pocketcfo's handleIndex) always recomputes
				// totals from the current invoice right before checking
				// IsCurrent.
				inv.Lines[0].Description = invoice.LocalizedString{De: strp("Andere Arbeit"), Bg: strp("Друга работа")}
				totals, err = money.Compute(inv)
				if err != nil {
					t.Fatal(err)
				}
				return inv, totals, manifest
			},
			wantCurrent: false,
		},
		{
			name: "render error is not current",
			setup: func(t *testing.T) (*invoice.InvoiceJson, money.Totals, Manifest) {
				inv := stalenessFixture("INV-0000000001", invoice.InvoiceJsonStatusIssued)
				inv.Language = invoice.InvoiceJsonLanguageEn // no "en" key on the description below -> HTML() errors
				totals, err := money.Compute(inv)
				if err != nil {
					t.Fatal(err)
				}
				return inv, totals, Manifest{"INV-0000000001.pdf": "irrelevant"}
			},
			wantCurrent: false,
		},
		{
			name: "paid variant mismatch",
			setup: func(t *testing.T) (*invoice.InvoiceJson, money.Totals, Manifest) {
				inv := stalenessFixture("INV-0000000001", invoice.InvoiceJsonStatusIssued)
				paid := mustDate("2026-02-01")
				inv.Paid = &paid

				totals, err := money.Compute(inv)
				if err != nil {
					t.Fatal(err)
				}
				mainHTML, err := HTML(inv, totals, false)
				if err != nil {
					t.Fatal(err)
				}
				// Main variant's hash is correct; the paid variant's is
				// deliberately wrong.
				return inv, totals, Manifest{
					"INV-0000000001.pdf":      HashHTML(mainHTML),
					"INV-0000000001-paid.pdf": "wrong-hash",
				}
			},
			wantCurrent: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chdirRepoRoot(t)
			inv, totals, manifest := tt.setup(t)
			if got := IsCurrent(inv, totals, manifest); got != tt.wantCurrent {
				t.Errorf("IsCurrent = %v, want %v", got, tt.wantCurrent)
			}
		})
	}
}
