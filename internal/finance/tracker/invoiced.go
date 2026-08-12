package tracker

import (
	"sort"
	"time"
)

const UnscopedClientID = 0

type InvoicedFact struct {
	ClientID   int
	Number     string
	IssueDate  time.Time
	DueDate    time.Time
	TotalCents int
}

type UsableInvoice struct {
	Number string
	Cents  int
}

type InvoicedClient struct {
	Horizon yearMonth
	Usable  map[yearMonth][]UsableInvoice
}

func (c InvoicedClient) suppresses(ym yearMonth) bool {
	return ym.ordinal() <= c.Horizon.ordinal()
}

func ComputeInvoiced(facts []InvoicedFact) map[int]InvoicedClient {
	byClient := map[int][]InvoicedFact{}
	for _, f := range facts {
		byClient[f.ClientID] = append(byClient[f.ClientID], f)
	}

	out := make(map[int]InvoicedClient, len(byClient))
	for cid, fs := range byClient {
		sort.Slice(fs, func(i, j int) bool { return fs[i].IssueDate.Before(fs[j].IssueDate) })
		latest := fs[len(fs)-1]
		horizon := yearMonth{latest.IssueDate.Year(), latest.IssueDate.Month()}.addMonths(-1)

		usable := map[yearMonth][]UsableInvoice{}
		for _, f := range fs {
			um := yearMonth{f.DueDate.Year(), f.DueDate.Month()}.addMonths(1)
			usable[um] = append(usable[um], UsableInvoice{Number: f.Number, Cents: f.TotalCents})
		}
		out[cid] = InvoicedClient{Horizon: horizon, Usable: usable}
	}
	return out
}

func (t *Tracker) invoiceSuppresses(clientID int, ym yearMonth) bool {
	if clientID == UnscopedClientID {
		return false
	}
	c, ok := t.Invoiced[clientID]
	return ok && c.suppresses(ym)
}

func (t *Tracker) invoicedCentsForMonth(ym yearMonth) int {
	var total int
	for _, c := range t.Invoiced {
		for _, inv := range c.Usable[ym] {
			total += inv.Cents
		}
	}
	return total
}

func (t *Tracker) invoicedInvoicesForMonth(ym yearMonth) []UsableInvoice {
	var out []UsableInvoice
	for _, c := range t.Invoiced {
		out = append(out, c.Usable[ym]...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}
