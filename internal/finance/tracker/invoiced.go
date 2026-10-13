package tracker

import (
	"sort"
	"time"
)

type InvoicedFact struct {
	Number     string
	IssueDate  time.Time
	DueDate    time.Time
	TotalCents int

	// IncomeMonth overrides the default rule when set: the month this
	// invoice counts as income, YYYY-MM, written down by the owner in
	// data/usable-invoices.json. Empty means the default applies — the
	// month after the due date.
	IncomeMonth string
}

type UsableInvoice struct {
	Number string
	Day    string
	Cents  int
}

type InvoicedClient struct {
	Usable map[yearMonth][]UsableInvoice
}

func ComputeInvoiced(facts []InvoicedFact) InvoicedClient {
	usable := map[yearMonth][]UsableInvoice{}
	for _, f := range facts {
		um := f.usableIn()
		usable[um] = append(usable[um], UsableInvoice{Number: f.Number, Day: f.DueDate.Format("02.01."), Cents: f.TotalCents})
	}
	return InvoicedClient{Usable: usable}
}

// usableIn is the month an invoice counts as income. The default rule: the
// month after it fell due. The owner can overwrite it per invoice in
// data/usable-invoices.json — an early-paying client, a December invoice
// booked in January — and the written-down month wins.
func (f InvoicedFact) usableIn() yearMonth {
	if f.IncomeMonth != "" {
		if parsed, err := time.Parse("2006-01", f.IncomeMonth); err == nil {
			return yearMonth{parsed.Year(), parsed.Month()}
		}
	}
	return yearMonth{f.DueDate.Year(), f.DueDate.Month()}.addMonths(1)
}

func (t *Tracker) invoicedCentsForMonth(ym yearMonth) int {
	var total int
	for _, inv := range t.Invoiced.Usable[ym] {
		total += inv.Cents
	}
	return total
}

func (t *Tracker) invoicedInvoicesForMonth(ym yearMonth) []UsableInvoice {
	out := append([]UsableInvoice(nil), t.Invoiced.Usable[ym]...)
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}
