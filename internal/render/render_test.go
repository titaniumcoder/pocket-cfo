package render

import (
	"encoding/json"
	"html/template"
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
			// relative to the repo root, so chdir there like pocket-cfo-ctl does.
			wd, _ := os.Getwd()
			t.Cleanup(func() { os.Chdir(wd) })
			if err := os.Chdir(filepath.Join("..", "..")); err != nil {
				t.Fatal(err)
			}

			html, err := HTML(inv, totals, nil)
			if err != nil {
				t.Fatalf("HTML: %v", err)
			}
			out := string(html)

			if strings.Contains(out, "BEZAHLT") {
				t.Error("the original (paidOn=nil) must never show the paid badge/stamp, however the invoice was actually settled")
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

// TestHTML_PaidVariant covers the -paid.pdf artifact: a non-nil paidOn must
// show the badge, the rotated stamp with that date, and a zeroed amount due
// — the mirror image of the paidOn=nil assertions above. The date is passed
// in rather than read off the invoice, which no longer carries one.
func TestHTML_PaidVariant(t *testing.T) {
	inv := loadFixture(t, "INV-0000000001")
	paidOn := mustDate("2026-01-15")

	totals, err := money.Compute(inv)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}

	html, err := HTML(inv, totals, &paidOn)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := string(html)

	if !strings.Contains(out, "BEZAHLT") {
		t.Error("the paid variant must show the paid badge/stamp")
	}
	if !strings.Contains(out, FormatDate(paidOn)) {
		t.Errorf("paid variant must show the payment date %q", FormatDate(paidOn))
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
	html, err := HTML(inv, totals, nil)
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
	html, err := HTML(inv, totals, nil)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if !strings.Contains(string(html), `class="notes"`) {
		t.Error("rendered HTML missing the notes box for a fixture with a real tax note")
	}
}

// TestHTML_LineDescriptionRendersRichText: a line description may carry
// newlines, bold and bullets; they render as line breaks and a list, and a
// multi-line description forces the block layout instead of the inline one.
func TestHTML_LineDescriptionRendersRichText(t *testing.T) {
	inv := loadFixture(t, "INV-0000000001")
	inv.Lines[0].Description.De = strptr("Migration of the mail server:\n- **export** the mailboxes\n- reimport them\nHandover *in person*")
	totals, err := money.Compute(inv)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}

	html, err := HTML(inv, totals, nil)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := string(html)

	for _, want := range []string{
		"Migration of the mail server:<br>",
		"<strong>export</strong> the mailboxes",
		"<em>in person</em>",
		"<li>reimport them</li>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered invoice lacks %q", want)
		}
	}
	if strings.Contains(out, "**") {
		t.Error("the raw markdown markers reached the page")
	}
	if strings.Contains(out, `<span class="desc-bg-inline">`) {
		t.Error("a multi-line description still rendered in the single-line layout")
	}
}

func strptr(s string) *string { return &s }

// TestHTML_CarriesTheRunningHeaderForContinuationPages: the paged.js running
// element, its column row and the per-page stamp handler must all ship in the
// rendered HTML — the page-2 letterhead lives in these pieces, not anywhere
// else.
func TestHTML_CarriesTheRunningHeaderForContinuationPages(t *testing.T) {
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

	html, err := HTML(inv, totals, nil)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := string(html)

	for _, want := range []string{
		"position: running(running-header)",
		"content: element(running-header)",
		"@page :first",
		`class="running-header"`,
		"RepeatColumns",
		"PageStamps",
		inv.Issuer.LegalName,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

// TestHTML_TextLineSpansTheWholeTable: a type=text line renders as one
// full-width prose row — no quantity, unit price, VAT or amount cells — and
// keeps its place in the line order.
func TestHTML_TextLineSpansTheWholeTable(t *testing.T) {
	inv := loadFixture(t, "INV-0000000001")
	inv.Lines[0].Type = invoice.LineTypeText
	totals, err := money.Compute(inv)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}

	html, err := HTML(inv, totals, nil)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := string(html)

	if strings.Count(out, `<div class="textline">`) != 1 {
		t.Errorf("want exactly one full-width text block between table segments")
	}
	if strings.Count(out, "<table>") != 2 {
		t.Errorf("the text line must split the table into two segments, got %d tables", strings.Count(out, "<table>"))
	}
	if strings.Contains(out, "colspan") {
		t.Error("a colspan row is back — paged.js cannot split one, and the whole table jumps pages")
	}
}

// TestRunningLogoRewritesIds: the running header's logo copy carries its own
// ids. Two svgs sharing gradient ids resolve their url(#…) references against
// whichever copy sorts first in the document — the page-1 logo went invisible
// exactly that way once the running copy existed.
func TestRunningLogoRewritesIds(t *testing.T) {
	in := template.HTML(`<svg id="Layer_1"><defs><linearGradient id="g1"/></defs><path fill="url(#g1)" xlink:href="#a" href="#a"/></svg>`)
	out := string(runningLogo(in))
	for _, bad := range []string{`id="Layer_1"`, `id="g1"`, `url(#g1)`, `href="#a"`} {
		if strings.Contains(out, bad) {
			t.Errorf("rewritten logo still contains %q", bad)
		}
	}
	for _, want := range []string{`id="rh-Layer_1"`, `url(#rh-g1)`, `href="#rh-a"`} {
		if !strings.Contains(out, want) {
			t.Errorf("rewritten logo missing %q", want)
		}
	}
}
