package tracker

import (
	"sort"
	"time"
)

// UnscopedClientID is the InvoicedFact.ClientID sentinel for an invoice with
// no Toggl client linkage — either the recipient has no tracking_client_id,
// or Toggl isn't configured at all. Real Toggl client IDs are always
// positive, so 0 is safe to use as "not scoped to any specific client": the
// invoice's total still counts as income (invoicedCentsForMonth sums
// indiscriminately across every key, including this one), but it never
// suppresses a specific client's tracked/predicted hours (invoiceSuppresses
// checks one concrete clientID at a time, which is never 0 for a real
// Toggl client).
const UnscopedClientID = 0

// InvoicedFact is the minimal, schema-decoupled fact Tracker needs about one
// issued invoice linked (via its recipient's tracking_client_id) to a
// Toggl client — built by the caller from the real invoice/recipient JSON
// (internal/schema/invoice, internal/schema/recipient; see Phase 5's
// wiring). This package has no dependency on that schema's shape, same
// reasoning as Aggregate/YearData staying Toggl's own types rather than
// something generic.
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
	// Horizon is the last calendar month (inclusive) whose Toggl
	// tracked/predicted contribution for this client is suppressed — the
	// month before the most recent invoice's IssueDate. "An invoice issued
	// 5 August supersedes this client's tracked/predicted hours through
	// end of July" — see PocketCFO's plan §2.3.
	Horizon yearMonth
	// Usable lists, by the single calendar month each invoice becomes
	// usable — the month after DueDate ("the money is usable in the month
	// following the due date") — every invoice whose usable month falls
	// there. Multiple invoices can coincide in the same month.
	Usable map[yearMonth][]UsableInvoice
}

// suppresses reports whether ym's Toggl contribution for this client
// should be suppressed: true for ym at or before Horizon.
func (c InvoicedClient) suppresses(ym yearMonth) bool {
	return ym.ordinal() <= c.Horizon.ordinal()
}

// ComputeInvoiced groups facts by ClientID into one InvoicedClient each.
// A client with no facts has no entry at all (callers should treat a
// missing key the same as "never invoiced" — nothing suppressed, nothing
// usable). Facts with ClientID == UnscopedClientID are grouped together
// under that same sentinel key: their totals still count via
// invoicedCentsForMonth/invoicedInvoicesForMonth, but suppresses() is never
// checked against UnscopedClientID by any real client lookup.
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

// invoiceSuppresses reports whether clientID's Toggl contribution for ym
// should be suppressed — false (never suppressed) for a client with no
// InvoicedClient entry, including when t.Invoiced itself is nil (no
// invoice data wired in at all), and always false for UnscopedClientID
// (an unscoped invoice never suppresses any specific client's hours).
func (t *Tracker) invoiceSuppresses(clientID int, ym yearMonth) bool {
	if clientID == UnscopedClientID {
		return false
	}
	c, ok := t.Invoiced[clientID]
	return ok && c.suppresses(ym)
}

// invoicedCentsForMonth sums every invoice's Cents across every invoiced
// client (including UnscopedClientID) for calendar month ym — the real
// invoiced income that becomes usable in ym, regardless of which client (or
// no client) it came from.
func (t *Tracker) invoicedCentsForMonth(ym yearMonth) int {
	var total int
	for _, c := range t.Invoiced {
		for _, inv := range c.Usable[ym] {
			total += inv.Cents
		}
	}
	return total
}

// invoicedInvoicesForMonth flattens every invoiced client's contribution to
// calendar month ym into a single list, sorted by invoice Number, for
// display (see the Income panel's per-invoice redesign).
func (t *Tracker) invoicedInvoicesForMonth(ym yearMonth) []UsableInvoice {
	var out []UsableInvoice
	for _, c := range t.Invoiced {
		out = append(out, c.Usable[ym]...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}
