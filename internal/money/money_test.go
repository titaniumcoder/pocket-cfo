package money

import (
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

func intp(v int) *int       { return &v }
func strp(v string) *string { return &v }

func line(desc string, qty *int, unitPrice, vatRate int) invoice.Line {
	return invoice.Line{
		Description: invoice.LocalizedString{De: strp(desc), Bg: strp(desc)},
		Quantity:    qty,
		UnitPrice:   unitPrice,
		VatRate:     vatRate,
	}
}

// TestCompute_ReferenceInvoices pins the totals of the two canonical
// example invoices used throughout ARCHITECTURE.md. See ARCHITECTURE.md §9.
func TestCompute_ReferenceInvoices(t *testing.T) {
	t.Run("INV-0000000001", func(t *testing.T) {
		inv := &invoice.InvoiceJson{
			Lines: []invoice.Line{
				line("Support-Vertrag Jahresabo Beispiel-Software", intp(100), 100000, 0),
				line("Individualentwicklung Beispiel-Modul", intp(600), 10000, 0),
				line("Wartung Beispielsystem: Support für 2 Gerätetypen", intp(300), 100000, 0),
			},
		}
		got, err := Compute(inv)
		if err != nil {
			t.Fatal(err)
		}
		if got.Subtotal != 460000 {
			t.Errorf("subtotal = %d, want 460000", got.Subtotal)
		}
		if got.GrandTotal != 460000 {
			t.Errorf("grand total = %d, want 460000", got.GrandTotal)
		}
	})

	t.Run("INV-0000000002", func(t *testing.T) {
		inv := &invoice.InvoiceJson{
			Lines: []invoice.Line{
				line("Beispielprojekt - Betrieb und Weiterentwicklung", intp(13600), 7500, 0),
			},
		}
		got, err := Compute(inv)
		if err != nil {
			t.Fatal(err)
		}
		if got.Subtotal != 1020000 {
			t.Errorf("subtotal = %d, want 1020000", got.Subtotal)
		}
		if got.GrandTotal != 1020000 {
			t.Errorf("grand total = %d, want 1020000", got.GrandTotal)
		}
	})
}

// TestCompute_QuantityAbsent covers the "quantity absent ⇒ amount =
// unit_price" rule from ARCHITECTURE.md §3.4.
func TestCompute_QuantityAbsent(t *testing.T) {
	inv := &invoice.InvoiceJson{
		Lines: []invoice.Line{line("one-off", nil, 12345, 0)},
	}
	got, err := Compute(inv)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subtotal != 12345 {
		t.Errorf("subtotal = %d, want 12345", got.Subtotal)
	}
}

// TestCompute_StackedDiscounts pins the worked example from
// ARCHITECTURE.md §3.5: two discounts on a 10 200,00 € base leave
// 9 896,00 € — each applied against the running total, not the original
// subtotal.
func TestCompute_StackedDiscounts(t *testing.T) {
	percent := 200  // 2%
	amount := 10000 // 100,00 €
	inv := &invoice.InvoiceJson{
		Lines: []invoice.Line{line("work", intp(13600), 7500, 0)},
		Discounts: []invoice.Discount{
			{Label: invoice.LocalizedString{De: strp("Skonto"), Bg: strp("Skonto")}, Percent: &percent},
			{Label: invoice.LocalizedString{De: strp("Treuerabatt"), Bg: strp("Treuerabatt")}, Amount: &amount},
		},
	}
	got, err := Compute(inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Discounts) != 2 {
		t.Fatalf("len(discounts) = %d, want 2", len(got.Discounts))
	}
	if got.Discounts[0].Amount != 20400 {
		t.Errorf("skonto = %d, want 20400 (204,00 €)", got.Discounts[0].Amount)
	}
	if got.Discounts[1].Amount != 10000 {
		t.Errorf("treuerabatt = %d, want 10000 (100,00 €)", got.Discounts[1].Amount)
	}
	if got.Net != 989600 {
		t.Errorf("net = %d, want 989600 (9 896,00 €)", got.Net)
	}
	if got.GrandTotal != 989600 {
		t.Errorf("grand total = %d, want 989600", got.GrandTotal)
	}
}

// TestCompute_MultiRateProportionalDiscount covers a mixed-rate invoice
// where a sum discount must split proportionally across rate groups
// before VAT is computed, and VAT rounds once per group.
func TestCompute_MultiRateProportionalDiscount(t *testing.T) {
	percent := 1000 // 10%
	inv := &invoice.InvoiceJson{
		Lines: []invoice.Line{
			line("standard rate", intp(100), 80000, 20), // 800,00 € @ 20%
			line("zero rate", intp(100), 20000, 0),      // 200,00 € @ 0%
		},
		Discounts: []invoice.Discount{
			{Label: invoice.LocalizedString{De: strp("d"), Bg: strp("d")}, Percent: &percent},
		},
	}
	got, err := Compute(inv)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subtotal != 100000 {
		t.Fatalf("subtotal = %d, want 100000", got.Subtotal)
	}
	if got.Net != 90000 {
		t.Fatalf("net = %d, want 90000", got.Net)
	}
	if len(got.VATGroups) != 2 {
		t.Fatalf("len(vat groups) = %d, want 2", len(got.VATGroups))
	}
	// 800,00 discounted 10% -> 720,00 base @ 20% -> 144,00 VAT.
	if got.VATGroups[0].DiscountedBase != 72000 {
		t.Errorf("group0 discounted base = %d, want 72000", got.VATGroups[0].DiscountedBase)
	}
	if got.VATGroups[0].VAT != 14400 {
		t.Errorf("group0 vat = %d, want 14400", got.VATGroups[0].VAT)
	}
	// 200,00 discounted 10% -> 180,00 base @ 0% -> 0 VAT.
	if got.VATGroups[1].DiscountedBase != 18000 {
		t.Errorf("group1 discounted base = %d, want 18000", got.VATGroups[1].DiscountedBase)
	}
	sumBases := got.VATGroups[0].DiscountedBase + got.VATGroups[1].DiscountedBase
	if sumBases != got.Net {
		t.Errorf("sum of discounted bases = %d, want %d (net)", sumBases, got.Net)
	}
	if got.GrandTotal != got.Net+got.VAT {
		t.Errorf("grand total = %d, want net+vat = %d", got.GrandTotal, got.Net+got.VAT)
	}
}

// TestCompute_DiscountBelowZero rejects a running total driven negative.
func TestCompute_DiscountBelowZero(t *testing.T) {
	amount := 999999999
	inv := &invoice.InvoiceJson{
		Lines:     []invoice.Line{line("small", intp(100), 100, 0)},
		Discounts: []invoice.Discount{{Label: invoice.LocalizedString{De: strp("d"), Bg: strp("d")}, Amount: &amount}},
	}
	if _, err := Compute(inv); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestLabelOf covers the de-then-bg-then-empty fallback — used only for an
// error message, not a rendering guarantee, so it tolerates a partially
// filled LocalizedString that render.HTML's stricter Require would reject.
func TestLabelOf(t *testing.T) {
	tests := []struct {
		name string
		ls   invoice.LocalizedString
		want string
	}{
		{"de present", invoice.LocalizedString{De: strp("Skonto"), Bg: strp("Отстъпка")}, "Skonto"},
		{"de absent, bg present", invoice.LocalizedString{Bg: strp("Отстъпка")}, "Отстъпка"},
		{"neither present", invoice.LocalizedString{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := labelOf(tt.ls); got != tt.want {
				t.Errorf("labelOf(%+v) = %q, want %q", tt.ls, got, tt.want)
			}
		})
	}
}
