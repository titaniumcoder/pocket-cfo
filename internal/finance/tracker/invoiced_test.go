package tracker

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestComputeInvoicedUsableIsMonthAfterDueDate(t *testing.T) {
	facts := []InvoicedFact{
		{Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2026, time.September}
	usable := got.Usable[want]
	if len(usable) != 1 || usable[0] != (UsableInvoice{Number: "0001", Cents: 500000}) {
		t.Errorf("Usable[%+v] = %+v, want [{0001 500000}]", want, usable)
	}
}

func TestComputeInvoicedUsableCrossesYearBoundary(t *testing.T) {
	facts := []InvoicedFact{
		{Number: "0001", IssueDate: date(2026, 12, 1), DueDate: date(2026, 12, 15), TotalCents: 100000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2027, time.January}
	usable := got.Usable[want]
	if len(usable) != 1 || usable[0].Cents != 100000 {
		t.Errorf("Usable[%+v] = %+v, want one entry of 100000", want, usable)
	}
}

func TestComputeInvoicedUsableKeepsCoincidingMonthsAsSeparateEntries(t *testing.T) {
	// Two invoices with different due dates that both fall within August
	// — both become usable in the same month, September, but stay
	// separate line items (not summed away) so the display can list both.
	facts := []InvoicedFact{
		{Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 2), TotalCents: 100000},
		{Number: "0002", IssueDate: date(2026, 9, 1), DueDate: date(2026, 8, 28), TotalCents: 250000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2026, time.September}
	usable := got.Usable[want]
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

func TestInvoicedCentsForMonthSumsTheMonth(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{Number: "0002", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 300000},
	})}
	want := yearMonth{2026, time.September}
	if got := trk.invoicedCentsForMonth(want); got != 800000 {
		t.Errorf("invoicedCentsForMonth(%+v) = %d, want 800000", want, got)
	}
	if got := trk.invoicedCentsForMonth(yearMonth{2026, time.January}); got != 0 {
		t.Errorf("invoicedCentsForMonth(unrelated month) = %d, want 0", got)
	}
}

func TestInvoicedCentsForMonthNilInvoiced(t *testing.T) {
	var trk Tracker
	if got := trk.invoicedCentsForMonth(yearMonth{2026, time.January}); got != 0 {
		t.Errorf("invoicedCentsForMonth on a nil map = %d, want 0", got)
	}
}

func TestInvoicedInvoicesForMonthSortsByNumber(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{Number: "0002", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 300000},
		{Number: "0003", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 100000},
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
		{Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}
	if got := trk.invoicedInvoicesForMonth(yearMonth{2026, time.January}); len(got) != 0 {
		t.Errorf("invoicedInvoicesForMonth(unrelated month) = %+v, want empty", got)
	}
}
