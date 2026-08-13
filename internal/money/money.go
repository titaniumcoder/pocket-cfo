package money

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

type Line struct {
	Description invoice.LocalizedString
	Quantity    *int
	Unit        *string
	UnitPrice   int64
	VATRate     int
	Amount      int64
}

type Discount struct {
	Label   invoice.LocalizedString
	Percent *int
	Amount  int64
}

type VATGroup struct {
	Rate           int
	Base           int64
	DiscountedBase int64
	VAT            int64
}

type Totals struct {
	Lines      []Line
	Subtotal   int64
	Discounts  []Discount
	Net        int64
	VATGroups  []VATGroup
	VAT        int64
	GrandTotal int64
}

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

func labelOf(l invoice.LocalizedString) string {
	if s, ok := l.Get(invoice.InvoiceJsonLanguageDe); ok {
		return s
	}
	if s, ok := l.Get(invoice.InvoiceJsonLanguageBg); ok {
		return s
	}
	return ""
}
