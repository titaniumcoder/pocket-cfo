package validate

import (
	"strings"
	"testing"

	"github.com/atombender/go-jsonschema/pkg/types"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/notes"
)

func strp(s string) *string { return &s }

func date(t *testing.T, s string) types.SerializableDate {
	t.Helper()
	var d types.SerializableDate
	if err := d.UnmarshalJSON([]byte(`"` + s + `"`)); err != nil {
		t.Fatal(err)
	}
	return d
}

func atInvoice(t *testing.T) Doc {
	t.Helper()
	inv := &invoice.InvoiceJson{
		Number:    "INV-0000000002",
		Language:  invoice.InvoiceJsonLanguageDe,
		IssueDate: date(t, "2026-01-15"),
		DueDate:   date(t, "2026-01-22"),
		Issuer: invoice.IssuerSnapshot{
			Address: invoice.Address{CountryCode: "BG"},
			VatId:   "BG000000000",
		},
		Recipient: invoice.RecipientSnapshot{
			Address:    invoice.Address{CountryCode: "AT"},
			VatId:      strp("ATU00000000"),
			IsBusiness: true,
		},
		Lines: []invoice.Line{{
			Description: invoice.LocalizedString{De: strp("Arbeit"), Bg: strp("Работа")},
			UnitPrice:   100000,
			VatRate:     0,
		}},
		Tax: invoice.Tax{
			Regime: invoice.TaxRegimeEuB2BReverseCharge,
			Note: invoice.LocalizedString{
				De: strp("Steuerschuldnerschaft des Leistungsempfängers (Reverse Charge)."),
				Bg: strp("Данъкът е изискуем от получателя (обратно начисляване)."),
			},
		},
	}
	return Doc{Path: "data/invoices/INV-0000000002.json", Base: "INV-0000000002", Inv: inv}
}

func catalog() *notes.NotesJson {
	return &notes.NotesJson{Regimes: notes.NotesJsonRegimes{
		"eu_b2b_reverse_charge": notes.Entry{
			Text:             notes.LocalizedString{De: strp("Reverse Charge."), Bg: strp("обратно начисляване.")},
			MandatoryWording: []string{"обратно начисляване"},
		},
		"outside_eu_place_of_supply": notes.Entry{
			Text:             notes.LocalizedString{De: strp("Steuerfrei."), Bg: strp("Без данък.")},
			MandatoryWording: []string{},
		},
	}}
}

func TestInvoiceAcceptsAGoodDocument(t *testing.T) {
	if err := Invoice(atInvoice(t), catalog()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnEUB2BInvoiceMustPrintTheReverseChargeWording(t *testing.T) {
	d := atInvoice(t)
	d.Inv.Tax.Note.Bg = strp("Данъкът не е начислен на основание чл. 21, ал. 2 ЗДДС.")

	err := Invoice(d, catalog())
	if err == nil {
		t.Fatal("want an error for a reverse-charge invoice that never says so")
	}
	if !strings.Contains(err.Error(), "обратно начисляване") {
		t.Errorf("error = %q, want it to name the missing wording", err)
	}
}

func TestTheWordingIsSearchedInWhatThePagePrints(t *testing.T) {
	d := atInvoice(t)
	d.Inv.Tax.Note = invoice.LocalizedString{
		De: strp("Reverse Charge."),
		Bg: strp("Данъкът е изискуем от получателя."),
		En: strp("обратно начисляване"),
	}
	if err := Invoice(d, catalog()); err == nil {
		t.Error("want an error — the phrase is in a language this invoice does not print")
	}
}

func TestARegimeWithNoCatalogEntryIsRefused(t *testing.T) {
	d := atInvoice(t)
	cat := catalog()
	delete(cat.Regimes, "eu_b2b_reverse_charge")

	err := Invoice(d, cat)
	if err == nil || !strings.Contains(err.Error(), "no entry for regime") {
		t.Errorf("error = %v, want it to report the missing catalog entry", err)
	}
}

func TestTheStoredRegimeMustMatchTheParties(t *testing.T) {
	t.Run("a domestic recipient zero-rated as if abroad", func(t *testing.T) {
		d := atInvoice(t)
		d.Inv.Recipient.Address.CountryCode = "BG"
		d.Inv.Recipient.VatId = strp("BG123456789")

		err := Invoice(d, catalog())
		if err == nil {
			t.Fatal("want an error for a regime that contradicts the recipient")
		}
		for _, want := range []string{"eu_b2b_reverse_charge", "domestic_standard", "BG"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to mention %q", err, want)
			}
		}
	})

	t.Run("a consumer has no regime at all", func(t *testing.T) {
		d := atInvoice(t)
		d.Inv.Recipient.IsBusiness = false
		if err := Invoice(d, catalog()); err == nil {
			t.Error("want an error — this company invoices businesses only")
		}
	})

	t.Run("a zero-rated regime with a rated line", func(t *testing.T) {
		d := atInvoice(t)
		d.Inv.Lines[0].VatRate = 20
		err := Invoice(d, catalog())
		if err == nil || !strings.Contains(err.Error(), "charges no VAT") {
			t.Errorf("error = %v, want the contradiction reported", err)
		}
	})
}

func TestFilenameMustMatchTheNumber(t *testing.T) {
	d := atInvoice(t)
	d.Base = "INV-0000000007"
	err := Invoice(d, catalog())
	if err == nil || !strings.Contains(err.Error(), "INV-0000000007") {
		t.Errorf("error = %v, want the filename mismatch reported", err)
	}
}

func TestDueDateMayNotPrecedeIssueDate(t *testing.T) {
	d := atInvoice(t)
	d.Inv.DueDate = date(t, "2026-01-14")
	err := Invoice(d, catalog())
	if err == nil || !strings.Contains(err.Error(), "before issue_date") {
		t.Errorf("error = %v, want the ordering reported", err)
	}

	t.Run("due on the day of issue is fine", func(t *testing.T) {
		d := atInvoice(t)
		d.Inv.DueDate = d.Inv.IssueDate
		if err := Invoice(d, catalog()); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestDiscountsMayNotDriveTheTotalBelowZero(t *testing.T) {
	d := atInvoice(t)
	amount := 999999999
	d.Inv.Discounts = []invoice.Discount{{
		Label:  invoice.LocalizedString{De: strp("Rabatt"), Bg: strp("Отстъпка")},
		Amount: &amount,
	}}
	err := Invoice(d, catalog())
	if err == nil || !strings.Contains(err.Error(), "below zero") {
		t.Errorf("error = %v, want the below-zero total reported", err)
	}
}

func TestInvoiceReportsEveryBreachAtOnce(t *testing.T) {
	d := atInvoice(t)
	d.Base = "INV-0000000009"
	d.Inv.DueDate = date(t, "2026-01-01")
	d.Inv.Tax.Note.Bg = strp("нищо")

	err := Invoice(d, catalog())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"INV-0000000009", "before issue_date", "обратно начисляване"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to also report %q", err, want)
		}
	}
}

func TestInvoiceSet(t *testing.T) {
	doc := func(number, path string) Doc {
		return Doc{Path: path, Base: number, Inv: &invoice.InvoiceJson{Number: number}}
	}

	t.Run("a gapless sequence passes", func(t *testing.T) {
		docs := []Doc{doc("INV-0000000002", "b.json"), doc("INV-0000000001", "a.json")}
		if err := InvoiceSet(docs); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("one missing number", func(t *testing.T) {
		docs := []Doc{doc("INV-0000000001", "a.json"), doc("INV-0000000003", "c.json")}
		err := InvoiceSet(docs)
		if err == nil || !strings.Contains(err.Error(), "INV-0000000002 is missing") {
			t.Errorf("error = %v, want the missing number named", err)
		}
	})

	t.Run("a run of missing numbers is one error, not ten", func(t *testing.T) {
		docs := []Doc{doc("INV-0000000001", "a.json"), doc("INV-0000000012", "l.json")}
		err := InvoiceSet(docs)
		if err == nil {
			t.Fatal("want an error")
		}
		if got := strings.Count(err.Error(), "\n") + 1; got != 1 {
			t.Errorf("%d errors for one gap, want 1:\n%v", got, err)
		}
		if !strings.Contains(err.Error(), "INV-0000000002 to INV-0000000011 are missing") {
			t.Errorf("error = %q, want the whole run named", err)
		}
	})

	t.Run("a duplicate number names both files", func(t *testing.T) {
		docs := []Doc{doc("INV-0000000001", "a.json"), doc("INV-0000000001", "copy.json")}
		err := InvoiceSet(docs)
		if err == nil {
			t.Fatal("want an error")
		}
		for _, want := range []string{"a.json", "copy.json"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("the sequence starts at 1", func(t *testing.T) {
		docs := []Doc{doc("INV-0000000004", "d.json"), doc("INV-0000000005", "e.json")}
		err := InvoiceSet(docs)
		if err == nil || !strings.Contains(err.Error(), "starts at INV-0000000004") {
			t.Errorf("error = %v, want the start reported", err)
		}
	})
}

func TestCatalog(t *testing.T) {
	t.Run("the shipped shape passes", func(t *testing.T) {
		if err := Catalog(catalog()); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("a note with no bg text", func(t *testing.T) {
		c := catalog()
		entry := c.Regimes["eu_b2b_reverse_charge"]
		entry.Text.Bg = nil
		c.Regimes["eu_b2b_reverse_charge"] = entry
		if err := Catalog(c); err == nil {
			t.Error("want an error — every invoice prints the bg column")
		}
	})

	t.Run("an empty mandatory phrase", func(t *testing.T) {
		c := catalog()
		entry := c.Regimes["eu_b2b_reverse_charge"]
		entry.MandatoryWording = []string{"  "}
		c.Regimes["eu_b2b_reverse_charge"] = entry
		if err := Catalog(c); err == nil {
			t.Error("want an error — an empty phrase satisfies every note")
		}
	})
}
