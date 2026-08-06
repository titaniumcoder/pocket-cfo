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
		{ProjectID: 1, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	}
	got := ComputeInvoiced(facts)
	p, ok := got[1]
	if !ok {
		t.Fatal("expected an entry for project 1")
	}
	if want := (yearMonth{2026, time.July}); p.Horizon != want {
		t.Errorf("Horizon = %+v, want %+v (end of month before the 5 Aug issue date)", p.Horizon, want)
	}
}

func TestComputeInvoicedHorizonUsesMostRecentInvoice(t *testing.T) {
	// Two invoices for the same project: an earlier one (May) and a later
	// one (September) — the horizon must track the later one, not the
	// first one found or the smallest.
	facts := []InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 5, 3), DueDate: date(2026, 5, 17), TotalCents: 100000},
		{ProjectID: 1, IssueDate: date(2026, 9, 2), DueDate: date(2026, 9, 16), TotalCents: 200000},
	}
	got := ComputeInvoiced(facts)
	if want := (yearMonth{2026, time.August}); got[1].Horizon != want {
		t.Errorf("Horizon = %+v, want %+v (end of month before the later, September, invoice)", got[1].Horizon, want)
	}
}

func TestComputeInvoicedUsableCentsIsMonthAfterDueDate(t *testing.T) {
	facts := []InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2026, time.September}
	if cents := got[1].UsableCents[want]; cents != 500000 {
		t.Errorf("UsableCents[%+v] = %d, want 500000", want, cents)
	}
}

func TestComputeInvoicedUsableCentsCrossesYearBoundary(t *testing.T) {
	facts := []InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 12, 1), DueDate: date(2026, 12, 15), TotalCents: 100000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2027, time.January}
	if cents := got[1].UsableCents[want]; cents != 100000 {
		t.Errorf("UsableCents[%+v] = %d, want 100000", want, cents)
	}
}

func TestComputeInvoicedUsableCentsSumsCoincidingMonths(t *testing.T) {
	// Two invoices with different due dates that both fall within August
	// — both become usable in the same month, September.
	facts := []InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 2), TotalCents: 100000},
		{ProjectID: 1, IssueDate: date(2026, 9, 1), DueDate: date(2026, 8, 28), TotalCents: 250000},
	}
	got := ComputeInvoiced(facts)
	want := yearMonth{2026, time.September}
	if cents := got[1].UsableCents[want]; cents != 350000 {
		t.Errorf("UsableCents[%+v] = %d, want 350000 (both invoices' totals summed)", want, cents)
	}
}

func TestComputeInvoicedKeepsProjectsIndependent(t *testing.T) {
	facts := []InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{ProjectID: 2, IssueDate: date(2026, 3, 1), DueDate: date(2026, 3, 15), TotalCents: 100000},
	}
	got := ComputeInvoiced(facts)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 independent projects", len(got))
	}
	if got[1].Horizon == got[2].Horizon {
		t.Error("project 1 and project 2 should have independent horizons")
	}
}

func TestInvoiceSuppressesOnOrBeforeHorizon(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}

	cases := []struct {
		ym   yearMonth
		want bool
	}{
		{yearMonth{2026, time.June}, true},
		{yearMonth{2026, time.July}, true}, // the horizon month itself
		{yearMonth{2026, time.August}, false},
	}
	for _, c := range cases {
		if got := trk.invoiceSuppresses(1, c.ym); got != c.want {
			t.Errorf("invoiceSuppresses(1, %+v) = %v, want %v", c.ym, got, c.want)
		}
	}
}

func TestInvoiceSuppressesFalseForUnknownProjectOrNilMap(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})}
	if trk.invoiceSuppresses(999, yearMonth{2026, time.January}) {
		t.Error("a project with no invoices should never be suppressed")
	}

	var nilTrk Tracker // Invoiced left as the nil zero value
	if nilTrk.invoiceSuppresses(1, yearMonth{2026, time.January}) {
		t.Error("a nil Invoiced map should never suppress anything")
	}
}

func TestInvoicedCentsForMonthSumsAcrossProjects(t *testing.T) {
	trk := &Tracker{Invoiced: ComputeInvoiced([]InvoicedFact{
		{ProjectID: 1, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
		{ProjectID: 2, IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 300000},
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
