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
		um := yearMonth{f.DueDate.Year(), f.DueDate.Month()}.addMonths(1)
		usable[um] = append(usable[um], UsableInvoice{Number: f.Number, Day: f.DueDate.Format("02.01."), Cents: f.TotalCents})
	}
	return InvoicedClient{Usable: usable}
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
