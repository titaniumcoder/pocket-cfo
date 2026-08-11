package tracker

import (
	"sort"
	"time"
)

// UnscopedClientID marks an invoice with no Toggl client linkage. Real client
// IDs are always positive, so 0 is safe as "not scoped to any client": the
// total still counts as income, but it never suppresses a specific client's
// tracked hours.
const UnscopedClientID = 0

// InvoicedFact is the minimal fact Tracker needs about one issued invoice,
// built by the caller from the real invoice/recipient JSON. Deliberately
// schema-decoupled: this package doesn't depend on internal/schema's shape.
type InvoicedFact struct {
	ClientID   int
	Number     string
	IssueDate  time.Time
	DueDate    time.Time
	TotalCents int
}

// UsableInvoice is one invoice's contribution to a calendar month's usable
// invoiced income.
type UsableInvoice struct {
	Number string
	Cents  int
}

// InvoicedClient is one Toggl client's invoicing state, derived by
// ComputeInvoiced from its InvoicedFacts.
type InvoicedClient struct {
	// Horizon is the last month whose Toggl contribution for this client is
	// suppressed: the month before the most recent invoice's IssueDate. An
	// invoice issued 5 August supersedes tracked hours through end of July.
	Horizon yearMonth
	// Usable indexes invoices by the month they become usable, the month after
	// DueDate. Several invoices can coincide in one month.
	Usable map[yearMonth][]UsableInvoice
}

// suppresses reports whether ym's Toggl contribution for this client
// should be suppressed: true for ym at or before Horizon.
func (c InvoicedClient) suppresses(ym yearMonth) bool {
	return ym.ordinal() <= c.Horizon.ordinal()
}

// ComputeInvoiced groups facts by ClientID into one InvoicedClient each. A
// missing key means "never invoiced": nothing suppressed, nothing usable.
// UnscopedClientID facts group under that sentinel — their totals still count,
// but no real client lookup ever checks suppresses() against it.
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

// invoiceSuppresses is false for a client with no entry, for a nil t.Invoiced,
// and always for UnscopedClientID.
func (t *Tracker) invoiceSuppresses(clientID int, ym yearMonth) bool {
	if clientID == UnscopedClientID {
		return false
	}
	c, ok := t.Invoiced[clientID]
	return ok && c.suppresses(ym)
}

// invoicedCentsForMonth sums every client's usable invoices for ym, including
// UnscopedClientID.
func (t *Tracker) invoicedCentsForMonth(ym yearMonth) int {
	var total int
	for _, c := range t.Invoiced {
		for _, inv := range c.Usable[ym] {
			total += inv.Cents
		}
	}
	return total
}

// invoicedInvoicesForMonth flattens ym's contributions into one list, sorted by
// invoice Number, for display.
func (t *Tracker) invoicedInvoicesForMonth(ym yearMonth) []UsableInvoice {
	var out []UsableInvoice
	for _, c := range t.Invoiced {
		out = append(out, c.Usable[ym]...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}
