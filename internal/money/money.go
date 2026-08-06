// Package money computes invoice totals in integer minor units. Nothing
// here is stored on the invoice — see ARCHITECTURE.md §3.4/§3.5. All
// intermediate arithmetic uses shopspring/decimal; rounding to minor units
// happens only at the points described there (line amounts, discounts,
// VAT), never on an intermediate sum.
package money

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// Line is one invoice line with its computed amount, in minor units.
type Line struct {
	Description invoice.LocalizedString
	Quantity    *int
	Unit        *string
	UnitPrice   int64
	VATRate     int
	Amount      int64
}

// Discount is one applied discount, with the amount it deducted from the
// running total, in minor units.
type Discount struct {
	Label   invoice.LocalizedString
	Percent *int
	Amount  int64
}

// VATGroup is the taxable base and VAT amount for one vat_rate, in minor
// units. Order matches first appearance of the rate among the lines.
type VATGroup struct {
	Rate           int
	Base           int64
	DiscountedBase int64
	VAT            int64
}

// Totals is the full computed breakdown of an invoice.
type Totals struct {
	Lines      []Line
	Subtotal   int64
	Discounts  []Discount
	Net        int64
	VATGroups  []VATGroup
	VAT        int64
	GrandTotal int64
}

// Compute derives all totals for inv. Pure function, no I/O.
//
// quantity is stored scaled by 100 (like money), so a line's real quantity
// is quantity/100 — quantity absent means a real quantity of 1. See
// ARCHITECTURE.md §3.4.
func Compute(inv *invoice.InvoiceJson) (Totals, error) {
	lines := make([]Line, 0, len(inv.Lines))
	var subtotal int64
	for _, l := range inv.Lines {
		qty := decimal.NewFromInt(1)
		if l.Quantity != nil {
			qty = decimal.NewFromInt(int64(*l.Quantity)).Div(decimal.NewFromInt(100))
		}
		amount := roundHalfAwayFromZero(qty.Mul(decimal.NewFromInt(int64(l.UnitPrice))))
		lines = append(lines, Line{
			Description: l.Description,
			Quantity:    l.Quantity,
			Unit:        l.Unit,
			UnitPrice:   int64(l.UnitPrice),
			VATRate:     l.VatRate,
			Amount:      amount,
		})
		subtotal += amount
	}

	discounts := make([]Discount, 0, len(inv.Discounts))
	running := subtotal
	for _, d := range inv.Discounts {
		var deduct int64
		switch {
		case d.Percent != nil:
			deduct = roundHalfAwayFromZero(
				decimal.NewFromInt(running).Mul(decimal.NewFromInt(int64(*d.Percent))).Div(decimal.NewFromInt(10000)),
			)
		case d.Amount != nil:
			deduct = int64(*d.Amount)
		default:
			return Totals{}, fmt.Errorf("discount %q: neither percent nor amount set", labelOf(d.Label))
		}
		running -= deduct
		discounts = append(discounts, Discount{Label: d.Label, Percent: d.Percent, Amount: deduct})
	}
	net := running
	if net < 0 {
		return Totals{}, fmt.Errorf("discounts reduce total below zero: net = %d", net)
	}
	totalDiscount := subtotal - net

	groups := vatGroups(lines)
	allocateDiscount(groups, subtotal, totalDiscount, net)

	var vatTotal int64
	for i := range groups {
		groups[i].VAT = roundHalfAwayFromZero(
			decimal.NewFromInt(groups[i].DiscountedBase).Mul(decimal.NewFromInt(int64(groups[i].Rate))).Div(decimal.NewFromInt(100)),
		)
		vatTotal += groups[i].VAT
	}

	return Totals{
		Lines:      lines,
		Subtotal:   subtotal,
		Discounts:  discounts,
		Net:        net,
		VATGroups:  groups,
		VAT:        vatTotal,
		GrandTotal: net + vatTotal,
	}, nil
}

// vatGroups sums line amounts per vat_rate, preserving first-appearance
// order — the order they're shown in on the invoice.
func vatGroups(lines []Line) []VATGroup {
	var groups []VATGroup
	index := map[int]int{}
	for _, l := range lines {
		i, ok := index[l.VATRate]
		if !ok {
			i = len(groups)
			index[l.VATRate] = i
			groups = append(groups, VATGroup{Rate: l.VATRate})
		}
		groups[i].Base += l.Amount
	}
	return groups
}

// allocateDiscount splits totalDiscount across groups proportionally to
// each group's share of subtotal, so per-rate bases sum to net. The last
// group absorbs the rounding remainder. See ARCHITECTURE.md §3.5.
func allocateDiscount(groups []VATGroup, subtotal, totalDiscount, net int64) {
	if len(groups) == 0 {
		return
	}
	if len(groups) == 1 || totalDiscount == 0 {
		groups[0].DiscountedBase = groups[0].Base - totalDiscount
		for i := 1; i < len(groups); i++ {
			groups[i].DiscountedBase = groups[i].Base
		}
		return
	}
	var allocated int64
	for i := 0; i < len(groups)-1; i++ {
		share := roundHalfAwayFromZero(
			decimal.NewFromInt(totalDiscount).Mul(decimal.NewFromInt(groups[i].Base)).Div(decimal.NewFromInt(subtotal)),
		)
		groups[i].DiscountedBase = groups[i].Base - share
		allocated += groups[i].DiscountedBase
	}
	groups[len(groups)-1].DiscountedBase = net - allocated
}

func roundHalfAwayFromZero(d decimal.Decimal) int64 {
	return d.Round(0).IntPart()
}

// labelOf picks a display string for an error message — not a rendering
// guarantee, so a simple de-then-bg-then-empty fallback is fine here even
// though render.HTML enforces the full translation-completeness rule
// (invoice.LocalizedString.Require) before actually rendering anything.
func labelOf(l invoice.LocalizedString) string {
	if s, ok := l.Get(invoice.InvoiceJsonLanguageDe); ok {
		return s
	}
	if s, ok := l.Get(invoice.InvoiceJsonLanguageBg); ok {
		return s
	}
	return ""
}
