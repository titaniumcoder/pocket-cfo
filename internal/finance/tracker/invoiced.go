package tracker

import (
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

const UnscopedClientID = 0

type InvoicedFact struct {
	Number     string
	ClientID   int
	IssueDate  time.Time
	DueDate    time.Time
	TotalCents int

	// TrackingUntil ends the invoice's coverage of Toggl trackings, up to
	// and including the day. Zero means coverage through the issue month.
	TrackingUntil time.Time

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
	Usable    map[yearMonth][]UsableInvoice
	Frontiers map[int]time.Time
}

func ComputeInvoiced(facts []InvoicedFact) InvoicedClient {
	usable := map[yearMonth][]UsableInvoice{}
	frontiers := map[int]time.Time{}
	for _, f := range facts {
		um := f.usableIn()
		usable[um] = append(usable[um], UsableInvoice{Number: f.Number, Day: f.DueDate.Format("02.01."), Cents: f.TotalCents})
		if f.ClientID == UnscopedClientID {
			continue
		}
		if ext := f.coverageExtent(); ext.After(frontiers[f.ClientID]) {
			frontiers[f.ClientID] = ext
		}
	}
	return InvoicedClient{Usable: usable, Frontiers: frontiers}
}

// coverageExtent is the last day the invoice covers. The rule lives with
// the invoice schema, where the invoicing page reads it for display too.
func (f InvoicedFact) coverageExtent() time.Time {
	return invoice.CoverageEnd(f.IssueDate, f.TrackingUntil)
}

// frontierFor is the client's coverage frontier — the last day its issued
// invoices already reach — or the zero time when it has none. A client with
// no linked recipient has no frontier: its hours cannot be matched to an
// invoice, so they stay estimated and the work panel flags them instead.
func (c InvoicedClient) frontierFor(clientID int) time.Time {
	if clientID == UnscopedClientID {
		return time.Time{}
	}
	return c.Frontiers[clientID]
}

// covers says whether the client's invoices already reach a tracking day.
func (c InvoicedClient) covers(clientID int, day time.Time) bool {
	return !day.After(c.frontierFor(clientID))
}

// coversMonth says whether the client's invoices cover a month whole.
func (c InvoicedClient) coversMonth(clientID int, monthEnd time.Time) bool {
	f := c.frontierFor(clientID)
	return !f.IsZero() && !f.Before(monthEnd)
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
