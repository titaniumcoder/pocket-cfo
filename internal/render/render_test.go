package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// loadFixture reads a real invoice from data/invoices. Defaults to the repo
// root relative to internal/render (two directories up) — the layout when
// code and data live in the same repo — but respects DATA_DIR like the app
// itself does, for the split layout where a companion repo runs these tests
// against its own real data via env var, code living in a submodule.
func loadFixture(t *testing.T, number string) *invoice.InvoiceJson {
	t.Helper()
	dir := getenv("DATA_DIR", filepath.Join("..", "..", "data"))
	path := filepath.Join(dir, "invoices", number+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var inv invoice.InvoiceJson
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return &inv
}

func TestHTML_RendersReferenceInvoices(t *testing.T) {
	wantCountry := map[string]string{
		"INV-0000000001": "Switzerland", // recipient CH
		"INV-0000000002": "Austria",     // recipient AT
	}

	for _, number := range []string{"INV-0000000001", "INV-0000000002"} {
		t.Run(number, func(t *testing.T) {
			inv := loadFixture(t, number)
			totals, err := money.Compute(inv)
			if err != nil {
				t.Fatalf("compute: %v", err)
			}
			noteDe, _ := inv.Tax.Note.Get(invoice.InvoiceJsonLanguageDe)
			noteBg, _ := inv.Tax.Note.Get(invoice.InvoiceJsonLanguageBg)
			// HTML() reads templates/invoice.html.tmpl and the logo SVG
			// relative to the repo root, so chdir there like invoicectl does.
			wd, _ := os.Getwd()
			t.Cleanup(func() { os.Chdir(wd) })
			if err := os.Chdir(filepath.Join("..", "..")); err != nil {
				t.Fatal(err)
			}

			html, err := HTML(inv, totals, false)
			if err != nil {
				t.Fatalf("HTML: %v", err)
			}
			out := string(html)

			if strings.Contains(out, "BEZAHLT") {
				t.Error("original (showPaid=false) must never show the paid badge/stamp, even when inv.Paid is set")
			}

			for _, want := range []string{
				inv.Number,
				inv.Issuer.LegalName,
				inv.Recipient.LegalName,
				noteDe,
				noteBg,
				FormatMoney(totals.GrandTotal),
				"RECHNUNG / ФАКТУРА", // both reference invoices are language: de
				"UID / ЕИК:",
				"USt-IdNr. / MWST-Nr. / ДДС №:",
				inv.Issuer.Bank.Name,
				"<svg",
				wantCountry[number],
			} {
				if !strings.Contains(out, want) {
					t.Errorf("rendered HTML missing %q", want)
				}
			}
			if strings.Contains(out, "<footer") {
				t.Error("rendered HTML still contains a <footer> element")
			}

			// The recipient's email is dropped (a spacer div replaces it).
			if strings.Contains(out, inv.Recipient.Email) {
				t.Errorf("rendered HTML still contains the recipient email %q", inv.Recipient.Email)
			}

			// The recipient block never shows a tax ID — only a VAT ID, and
			// only when set. This is a structural guarantee (no template
			// code path emits a recipient tax ID at all), checked by
			// counting occurrences rather than by value: exactly one
			// TaxIdLabel (the issuer's), and one VatIdLabel per party that
			// has a vat_id set. Both reference recipients currently have
			// one, so both fixtures expect two.
			if got := strings.Count(out, "UID / ЕИК:"); got != 1 {
				t.Errorf(`want exactly one tax-ID label (issuer only), got %d`, got)
			}
			if got := strings.Count(out, "USt-IdNr. / MWST-Nr. / ДДС №:"); got != 2 {
				t.Errorf(`want exactly two VAT-ID labels (issuer + recipient), got %d`, got)
			}
		})
	}
}

// TestHTML_PaidVariant covers the -paid.pdf artifact: showPaid=true must
// show the badge, the rotated stamp with the payment date, and a zeroed
// amount due — the mirror image of the showPaid=false assertions above.
func TestHTML_PaidVariant(t *testing.T) {
	inv := loadFixture(t, "INV-0000000001")
	if inv.Paid == nil {
		t.Fatal("fixture no longer has paid set — test is stale")
	}

	totals, err := money.Compute(inv)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}

	html, err := HTML(inv, totals, true)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := string(html)

	if !strings.Contains(out, "BEZAHLT") {
		t.Error("paid variant (showPaid=true) must show the paid badge/stamp")
	}
	if !strings.Contains(out, FormatDate(*inv.Paid)) {
		t.Errorf("paid variant must show the payment date %q", FormatDate(*inv.Paid))
	}
	if !strings.Contains(out, FormatMoney(0)) {
		t.Error("paid variant must show a zeroed amount due")
	}
}

// TestHTML_EmptyTaxNoteOmitsNotesBox covers the "no note needed" case: an
// entirely empty tax note must not render an empty labeled box.
func TestHTML_EmptyTaxNoteOmitsNotesBox(t *testing.T) {
	inv := loadFixture(t, "INV-0000000001")
	inv.Tax.Note = invoice.LocalizedString{}

	totals, err := money.Compute(inv)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	html, err := HTML(inv, totals, false)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if strings.Contains(string(html), `class="notes"`) {
		t.Error("rendered HTML still contains a notes box for an entirely empty tax note")
	}
}

// TestHTML_NonEmptyTaxNoteRendersNotesBox is the mirror check — a fixture
// with a real note must still show the box (guards against the {{if}}
// wrapper added for the empty case swallowing the normal case too).
func TestHTML_NonEmptyTaxNoteRendersNotesBox(t *testing.T) {
	inv := loadFixture(t, "INV-0000000001")
	totals, err := money.Compute(inv)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	html, err := HTML(inv, totals, false)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if !strings.Contains(string(html), `class="notes"`) {
		t.Error("rendered HTML missing the notes box for a fixture with a real tax note")
	}
}
