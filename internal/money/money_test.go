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

func TestCompute_ZeroSubtotalAcrossRateGroups(t *testing.T) {
	amount := 0
	inv := &invoice.InvoiceJson{
		Lines: []invoice.Line{
			line("free, zero-rated", nil, 0, 0),
			line("free, standard-rated", nil, 0, 20),
		},
		Discounts: []invoice.Discount{
			{Label: invoice.LocalizedString{De: strp("Rabatt"), Bg: strp("Rabatt")}, Amount: &amount},
		},
	}
	got, err := Compute(inv)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subtotal != 0 || got.Net != 0 || got.GrandTotal != 0 {
		t.Errorf("subtotal/net/grand = %d/%d/%d, want 0/0/0", got.Subtotal, got.Net, got.GrandTotal)
	}
	for _, g := range got.VATGroups {
		if g.DiscountedBase != 0 || g.VAT != 0 {
			t.Errorf("group %d%%: discounted base = %d, vat = %d, want 0 and 0", g.Rate, g.DiscountedBase, g.VAT)
		}
	}
}

func TestAllocateDiscount_ZeroSubtotalDoesNotPanic(t *testing.T) {
	groups := []VATGroup{{Rate: 0}, {Rate: 20}}
	allocateDiscount(groups, 0, -10000, 10000)
	if groups[0].DiscountedBase != 10000 {
		t.Errorf("first group discounted base = %d, want 10000", groups[0].DiscountedBase)
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

func TestCompute_TwoStackedTenPercentDiscountsTakeNineteen(t *testing.T) {
	first, second := 1000, 1000
	inv := &invoice.InvoiceJson{
		Lines: []invoice.Line{line("work", nil, 100000, 0)},
		Discounts: []invoice.Discount{
			{Label: invoice.LocalizedString{De: strp("Erst"), Bg: strp("Първа")}, Percent: &first},
			{Label: invoice.LocalizedString{De: strp("Zweit"), Bg: strp("Втора")}, Percent: &second},
		},
	}
	got, err := Compute(inv)
	if err != nil {
		t.Fatal(err)
	}
	if got.Discounts[0].Amount != 10000 {
		t.Errorf("first discount = %d, want 10000 (10%% of 1 000,00)", got.Discounts[0].Amount)
	}
	if got.Discounts[1].Amount != 9000 {
		t.Errorf("second discount = %d, want 9000 (10%% of the running 900,00, not 10000)", got.Discounts[1].Amount)
	}
	if got.Net != 81000 {
		t.Errorf("net = %d, want 81000 — two stacked 10%% discounts take 19%%, not 20%%", got.Net)
	}
	if taken := got.Subtotal - got.Net; taken != 19000 {
		t.Errorf("discount taken = %d of %d, want 19000 (19%%)", taken, got.Subtotal)
	}
}

func TestCompute_VATRoundsOncePerGroupNotPerLine(t *testing.T) {
	inv := &invoice.InvoiceJson{
		Lines: []invoice.Line{
			line("a", nil, 3, 20),
			line("b", nil, 3, 20),
		},
	}
	got, err := Compute(inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.VATGroups) != 1 {
		t.Fatalf("len(vat groups) = %d, want 1 — both lines share a rate", len(got.VATGroups))
	}
	if got.VATGroups[0].Base != 6 {
		t.Fatalf("group base = %d, want 6", got.VATGroups[0].Base)
	}
	if got.VAT != 1 {
		t.Errorf("vat = %d, want 1 — rounded once over the group's 6, not once per 3-unit line", got.VAT)
	}
	if got.GrandTotal != 7 {
		t.Errorf("grand total = %d, want 7", got.GrandTotal)
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

// TestLabelOf_Fallback covers the de-then-bg-then-empty fallback — used
// only for an error message, not a rendering guarantee, so it tolerates a
// partially filled LocalizedString that render.HTML's stricter Require
// would reject.
func TestLabelOf_Fallback(t *testing.T) {
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
