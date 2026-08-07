package tracker

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestComputeInvoicedHorizonIsEndOfMonthBeforeIssueDate(t *testing.T) {
	facts := []InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	}
	got := ComputeInvoiced(facts)
	c, ok := got[1]
	if !ok {
		t.Fatal("expected an entry for client 1")
	}
	if want := (yearMonth{2026, time.July}); c.Horizon != want {
		t.Errorf("Horizon = %+v, want %+v (end of month before the 5 Aug issue date)", c.Horizon, want)
	}
}

func TestComputeInvoicedHorizonUsesMostRecentInvoice(t *testing.T) {
	// Two invoices for the same client: an earlier one (May) and a later
	// one (September) — the horizon must track the later one, not the
	// first one found or the smallest.
	facts := []InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 5, 3), DueDate: date(2026, 5, 17), TotalCents: 100000},
		{ClientID: 1, Number: "0002", IssueDate: date(2026, 9, 2), DueDate: date(2026, 9, 16), TotalCents: 200000},
	}
	got := ComputeInvoiced(facts)
	if want := (yearMonth{2026, time.August}); got[1].Horizon != want {
		t.Errorf("Horizon = %+v, want %+v (end of month before the later, September, invoice)", got[1].Horizon, want)
	}
}

func TestComputeInvoicedUsableIsMonthAfterDueDate(t *testing.T) {
	facts := []InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2026, time.September}
	usable := got[1].Usable[want]
	if len(usable) != 1 || usable[0] != (UsableInvoice{Number: "0001", Cents: 500000}) {
		t.Errorf("Usable[%+v] = %+v, want [{0001 500000}]", want, usable)
	}
}

func TestComputeInvoicedUsableCrossesYearBoundary(t *testing.T) {
	facts := []InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 12, 1), DueDate: date(2026, 12, 15), TotalCents: 100000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2027, time.January}
	usable := got[1].Usable[want]
	if len(usable) != 1 || usable[0].Cents != 100000 {
		t.Errorf("Usable[%+v] = %+v, want one entry of 100000", want, usable)
	}
}

func TestComputeInvoicedUsableKeepsCoincidingMonthsAsSeparateEntries(t *testing.T) {
	// Two invoices with different due dates that both fall within August
	// — both become usable in the same month, September, but stay
	// separate line items (not summed away) so the display can list both.
	facts := []InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 2), TotalCents: 100000},
		{ClientID: 1, Number: "0002", IssueDate: date(2026, 9, 1), DueDate: date(2026, 8, 28), TotalCents: 250000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2026, time.September}
	usable := got[1].Usable[want]
	if len(usable) != 2 {
		t.Fatalf("Usable[%+v] = %+v, want 2 separate invoice entries", want, usable)
	}
	var total int
	for _, u := range usable {
		total += u.Cents
	}
	if total != 350000 {
		t.Errorf("total cents = %d, want 350000", total)
	}
}

func TestComputeInvoicedKeepsClientsIndependent(t *testing.T) {
	facts := []InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{ClientID: 2, Number: "0002", IssueDate: date(2026, 3, 1), DueDate: date(2026, 3, 15), TotalCents: 100000},
	}
	got := ComputeInvoiced(facts)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 independent clients", len(got))
	}
	if got[1].Horizon == got[2].Horizon {
		t.Error("client 1 and client 2 should have independent horizons")
	}
}

func TestComputeInvoicedGroupsUnscopedFactsTogether(t *testing.T) {
	facts := []InvoicedFact{
		{ClientID: UnscopedClientID, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{ClientID: UnscopedClientID, Number: "0002", IssueDate: date(2026, 3, 1), DueDate: date(2026, 3, 15), TotalCents: 100000},
	}
	got := ComputeInvoiced(facts)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (both facts grouped under UnscopedClientID)", len(got))
	}
	c, ok := got[UnscopedClientID]
	if !ok {
		t.Fatal("expected an entry keyed by UnscopedClientID")
	}
	// Horizon tracks the later (August) invoice.
	if want := (yearMonth{2026, time.July}); c.Horizon != want {
		t.Errorf("Horizon = %+v, want %+v", c.Horizon, want)
	}
}

func TestInvoiceSuppressesOnOrBeforeHorizon(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}

	tests := []struct {
		name string
		ym   yearMonth
		want bool
	}{
		{"before horizon", yearMonth{2026, time.June}, true},
		{"horizon month itself", yearMonth{2026, time.July}, true},
		{"after horizon", yearMonth{2026, time.August}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trk.invoiceSuppresses(1, tt.ym); got != tt.want {
				t.Errorf("invoiceSuppresses(1, %+v) = %v, want %v", tt.ym, got, tt.want)
			}
		})
	}
}

func TestInvoiceSuppressesFalseForUnknownClientOrNilMap(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}
	if trk.invoiceSuppresses(999, yearMonth{2026, time.January}) {
		t.Error("a client with no invoices should never be suppressed")
	}

	var nilTrk Tracker // Invoiced left as the nil zero value
	if nilTrk.invoiceSuppresses(1, yearMonth{2026, time.January}) {
		t.Error("a nil Invoiced map should never suppress anything")
	}
}

func TestInvoiceSuppressesNeverSuppressesUnscopedClientID(t *testing.T) {
	// Even if an unscoped invoice's horizon would otherwise cover this
	// month, UnscopedClientID must never suppress anything — it isn't
	// scoped to any specific client's tracked hours.
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ClientID: UnscopedClientID, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}
	if trk.invoiceSuppresses(UnscopedClientID, yearMonth{2026, time.June}) {
		t.Error("UnscopedClientID must never suppress, regardless of horizon")
	}
}

func TestInvoicedCentsForMonthSumsAcrossClients(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{ClientID: 2, Number: "0002", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 300000},
	})}
	want := yearMonth{2026, time.September}
	if got := trk.invoicedCentsForMonth(want); got != 800000 {
		t.Errorf("invoicedCentsForMonth(%+v) = %d, want 800000", want, got)
	}
	if got := trk.invoicedCentsForMonth(yearMonth{2026, time.January}); got != 0 {
		t.Errorf("invoicedCentsForMonth(unrelated month) = %d, want 0", got)
	}
}

func TestInvoicedCentsForMonthIncludesUnscopedInvoices(t *testing.T) {
	// An unscoped invoice (no Toggl, or recipient not linked to any Toggl
	// client) must still count as income even though it never suppresses
	// anything.
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ClientID: UnscopedClientID, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}
	want := yearMonth{2026, time.September}
	if got := trk.invoicedCentsForMonth(want); got != 500000 {
		t.Errorf("invoicedCentsForMonth(%+v) = %d, want 500000 (unscoped invoice still counts as income)", want, got)
	}
}

func TestInvoicedCentsForMonthNilInvoiced(t *testing.T) {
	var trk Tracker
	if got := trk.invoicedCentsForMonth(yearMonth{2026, time.January}); got != 0 {
		t.Errorf("invoicedCentsForMonth on a nil map = %d, want 0", got)
	}
}

func TestInvoicedInvoicesForMonthFlattensAndSortsByNumber(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ClientID: 1, Number: "0002", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{ClientID: 2, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 300000},
		{ClientID: UnscopedClientID, Number: "0003", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 100000},
	})}
	got := trk.invoicedInvoicesForMonth(yearMonth{2026, time.September})
	if len(got) != 3 {
		t.Fatalf("invoicedInvoicesForMonth = %+v, want 3 entries", got)
	}
	for i, want := range []string{"0001", "0002", "0003"} {
		if got[i].Number != want {
			t.Errorf("got[%d].Number = %q, want %q (sorted by Number)", i, got[i].Number, want)
		}
	}
}

func TestInvoicedInvoicesForMonthEmptyForUnrelatedMonth(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}
	if got := trk.invoicedInvoicesForMonth(yearMonth{2026, time.January}); len(got) != 0 {
		t.Errorf("invoicedInvoicesForMonth(unrelated month) = %+v, want empty", got)
	}
}
