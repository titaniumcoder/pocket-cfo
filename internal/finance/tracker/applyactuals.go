package tracker

import (
	"fmt"
	"time"
)

// Actual status values, in ranking order: a row gets the first that applies.
const (
	ActualMistimed   = "mistimed"
	ActualOver       = "over"
	ActualUnbudgeted = "unbudgeted"
	ActualUnder      = "under"
)

// MistimedRow is a one-off whose plan and payment fall in different months.
type MistimedRow struct {
	Name    string
	Note    string
	Cents   int
	Company bool
}

// ApplyActuals fills the actuals-only fields on an already-built view.
//
// It runs after buildBudgetView rather than inside it, and touches no planned
// figure. Budget.ForMonth also feeds monthBalanceDelta's account roll-forward,
// so keeping them apart is what makes actuals display-only by construction.
//
// charged maps category id to the months it was actually charged in across the
// year; nil in year view.
func ApplyActuals(bv *BudgetView, av ActualsView, viewed time.Month, charged map[string][]time.Month) {
	if bv == nil || !av.Present {
		return
	}
	applyToGroups(bv.Groups, av, viewed, charged)
	applyToGroups(bv.CompanyGroups, av, viewed, charged)
}

func applyToGroups(groups []CategoryGroupView, av ActualsView, viewed time.Month, charged map[string][]time.Month) {
	for gi := range groups {
		g := &groups[gi]
		for ri := range g.Rows {
			row := &g.Rows[ri]
			if cents, ok := av.ByCategory[row.CategoryID]; ok {
				row.ActualCents = cents
				row.HasActual = true
				g.ActualCents += cents
				g.HasActual = true
			}
			row.ActualStatus, row.ActualNote = actualStatus(*row, av.Complete, viewed, charged)
			if row.ActualStatus == ActualMistimed {
				g.HasMistimed = true
			}
			g.Status = worseStatus(g.Status, row.ActualStatus)
		}
	}
}

// onPlanTolerance is how far a figure may sit from its budget and still count
// as on plan: twenty euros, or five percent, whichever is larger. Below that
// the difference says nothing — a budget is a round number someone chose, not
// a measurement, and flagging 1 502 against 1 500 trains you to ignore the
// flag that matters.
func onPlanTolerance(plannedCents int) int {
	const floor = 2000 // €20
	if pct := plannedCents / 20; pct > floor {
		return pct
	}
	return floor
}

// statusRank orders the grades so a group can carry the one that most wants
// attention. Anything demanding action outranks the news that a category came
// in cheap.
func statusRank(status string) int {
	switch status {
	case ActualMistimed:
		return 4
	case ActualOver:
		return 3
	case ActualUnbudgeted:
		return 2
	case ActualUnder:
		return 1
	}
	return 0
}

func worseStatus(a, b string) string {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

// actualStatus grades one row. Over-plan fires immediately because no further
// statement data can make it untrue; under-plan waits for complete coverage,
// since on the 5th of the month every category is trivially under. Both only
// once the gap clears onPlanTolerance.
func actualStatus(row CategoryRow, coverageComplete bool, viewed time.Month, charged map[string][]time.Month) (status, note string) {
	if due, ok := plannedMonth(row.PlannedDate); ok && charged != nil {
		if row.HasActual && due != viewed {
			return ActualMistimed, fmt.Sprintf("planned for %s, charged now", due)
		}
		if !row.HasActual && due == viewed {
			if elsewhere, found := firstOtherMonth(charged[row.CategoryID], viewed); found {
				return ActualMistimed, fmt.Sprintf("planned here, already charged in %s", elsewhere)
			}
		}
	}

	if !row.HasActual {
		return "", ""
	}
	over := row.ActualCents - row.PlannedCents
	tolerance := onPlanTolerance(row.PlannedCents)
	switch {
	// No tolerance here: the band excuses a figure for missing a number
	// someone chose, and there is no number. Every unplanned charge is worth
	// seeing, because seeing it is how it gets budgeted next time.
	case row.PlannedCents == 0 && row.ActualCents > 0:
		return ActualUnbudgeted, ""
	case over > tolerance:
		return ActualOver, ""
	case -over > tolerance && coverageComplete:
		return ActualUnder, ""
	}
	return "", ""
}

func plannedMonth(date string) (time.Month, bool) {
	if date == "" {
		return 0, false
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	return d.Month(), true
}

func firstOtherMonth(months []time.Month, viewed time.Month) (time.Month, bool) {
	for _, m := range months {
		if m != viewed {
			return m, true
		}
	}
	return 0, false
}

// MistimedRowsOf collects every mistimed row across both ledgers, for the
// panel-level summary that keeps one from hiding inside a collapsed group.
func MistimedRowsOf(bv BudgetView) []MistimedRow {
	out := mistimedIn(bv.Groups, false)
	return append(out, mistimedIn(bv.CompanyGroups, true)...)
}

func mistimedIn(groups []CategoryGroupView, company bool) []MistimedRow {
	var out []MistimedRow
	for _, g := range groups {
		for _, row := range g.Rows {
			if row.ActualStatus != ActualMistimed {
				continue
			}
			cents := row.ActualCents
			if !row.HasActual {
				cents = row.PlannedCents
			}
			out = append(out, MistimedRow{Name: row.Name, Note: row.ActualNote, Cents: cents, Company: company})
		}
	}
	return out
}

// UnmatchedCents is recorded spending that reached no row: a category deleted
// since the month was reconciled, or a one-off whose month has passed. An id
// absent from budget.json has no kind and falls to private.
func UnmatchedCents(bv BudgetView, av ActualsView, companyIDs map[string]bool) (private, company int) {
	if !av.Present {
		return 0, 0
	}
	matched := map[string]bool{}
	for _, groups := range [][]CategoryGroupView{bv.Groups, bv.CompanyGroups} {
		for _, g := range groups {
			for _, row := range g.Rows {
				if row.HasActual {
					matched[row.CategoryID] = true
				}
			}
		}
	}
	for id, cents := range av.ByCategory {
		switch {
		case matched[id]:
		case companyIDs[id]:
			company += cents
		default:
			private += cents
		}
	}
	return private, company
}

// LedgerCtx pairs a ledger's groups with the page-level flags, which a nested
// template can't reach through $.
type LedgerCtx struct {
	Groups            []CategoryGroupView
	ShowActuals       bool
	SpendingDetailURL string
}

func (f Figures) PrivateLedger() LedgerCtx {
	return LedgerCtx{Groups: f.PrivateGroups, ShowActuals: f.ShowActuals, SpendingDetailURL: f.SpendingDetailURL}
}

func (f Figures) CompanyLedger() LedgerCtx {
	return LedgerCtx{Groups: f.FundingPersonal.CompanyGroups, ShowActuals: f.ShowActuals, SpendingDetailURL: f.SpendingDetailURL}
}
