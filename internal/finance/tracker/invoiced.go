package tracker

import (
	"sort"
	"time"
)

// InvoicedFact is the minimal, schema-decoupled fact Tracker needs about one
// issued invoice linked (via its recipient's tracking_project_id) to a
// Toggl project — built by the caller from the real invoice/recipient JSON
// (internal/schema/invoice, internal/schema/recipient; see Phase 5's
// wiring). This package has no dependency on that schema's shape, same
// reasoning as Aggregate/YearData staying Toggl's own types rather than
// something generic.
type InvoicedFact struct {
	ProjectID  int
	IssueDate  time.Time
	DueDate    time.Time
	TotalCents int
}

// InvoicedProject is one Toggl project's invoicing state, derived by
// ComputeInvoiced from its InvoicedFacts.
type InvoicedProject struct {
	// Horizon is the last calendar month (inclusive) whose Toggl
	// tracked/predicted contribution for this project is suppressed — the
	// month before the most recent invoice's IssueDate. "An invoice issued
	// 5 August supersedes this project's tracked/predicted hours through
	// end of July" — see PocketCFO's plan §2.3.
	Horizon yearMonth
	// UsableCents sums invoice totals by the single calendar month they
	// become usable — the month after DueDate ("the money is usable in the
	// month following the due date") — summed if more than one invoice's
	// usable month coincides.
	UsableCents map[yearMonth]int
}

// suppresses reports whether ym's Toggl contribution for this project
// should be suppressed: true for ym at or before Horizon.
func (p InvoicedProject) suppresses(ym yearMonth) bool {
	return ym.ordinal() <= p.Horizon.ordinal()
}

// ComputeInvoiced groups facts by ProjectID into one InvoicedProject each.
// A project with no facts has no entry at all (callers should treat a
// missing key the same as "never invoiced" — nothing suppressed, nothing
// usable).
func ComputeInvoiced(facts []InvoicedFact) map[int]InvoicedProject {
	byProject := map[int][]InvoicedFact{}
	for _, f := range facts {
		byProject[f.ProjectID] = append(byProject[f.ProjectID], f)
	}

	out := make(map[int]InvoicedProject, len(byProject))
	for pid, fs := range byProject {
		sort.Slice(fs, func(i, j int) bool { return fs[i].IssueDate.Before(fs[j].IssueDate) })
		latest := fs[len(fs)-1]
		horizon := yearMonth{latest.IssueDate.Year(), latest.IssueDate.Month()}.addMonths(-1)

		usable := map[yearMonth]int{}
		for _, f := range fs {
			um := yearMonth{f.DueDate.Year(), f.DueDate.Month()}.addMonths(1)
			usable[um] += f.TotalCents
		}
		out[pid] = InvoicedProject{Horizon: horizon, UsableCents: usable}
	}
	return out
}

// invoiceSuppresses reports whether projectID's Toggl contribution for ym
// should be suppressed — false (never suppressed) for a project with no
// InvoicedProject entry, including when t.Invoiced itself is nil (no
// invoice data wired in at all).
func (t *Tracker) invoiceSuppresses(projectID int, ym yearMonth) bool {
	p, ok := t.Invoiced[projectID]
	return ok && p.suppresses(ym)
}

// invoicedCentsForMonth sums UsableCents[ym] across every invoiced
// project — the real invoiced income that becomes usable in calendar month
// ym, regardless of which project(s) it came from.
func (t *Tracker) invoicedCentsForMonth(ym yearMonth) int {
	var total int
	for _, p := range t.Invoiced {
		total += p.UsableCents[ym]
	}
	return total
}
