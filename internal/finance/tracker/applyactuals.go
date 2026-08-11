package tracker

import (
	"fmt"
	"time"
)

// Actual status values, ranked: a row gets the first that applies.
const (
	// ActualMistimed means a one-off was charged in a month other than the
	// one it is budgeted for. It outranks everything else because it is the
	// only status saying the *plan* is wrong rather than that spending
	// differs from it.
	ActualMistimed = "mistimed"
	// ActualOver means more was spent than planned. Fires immediately: once
	// exceeded, no further statement data can make it untrue.
	ActualOver = "over"
	// ActualUnbudgeted means money went out against a category planned at
	// zero — typically one paused with an override.
	ActualUnbudgeted = "unbudgeted"
	// ActualUnder means less was spent than planned, and is deliberately
	// withheld until coverage is complete. Under-plan is the default state of
	// every category on the 5th of the month; a page of green then isn't a
	// finding, it's flattery, and once you've learned to discount it you
	// discount the red as well.
	ActualUnder = "under"
)

// MistimedRow is a one-off whose plan and payment fall in different months,
// surfaced at panel level so it can't hide inside a collapsed group.
type MistimedRow struct {
	Name    string
	Note    string
	Cents   int
	Company bool // company-kind, so the template can put it in the right ledger
}

// ApplyActuals fills the actuals-only fields on an already-built view.
//
// It deliberately runs *after* buildBudgetView rather than inside it, and
// never touches PlannedCents, the group totals or either view total. That is
// what makes actuals display-only by construction rather than by promise:
// Budget.ForMonth also feeds Tracker.monthBalanceDelta's account roll-forward,
// and nothing here can reach it.
//
// charged maps category id -> the months it was actually charged in across the
// whole year, which is what the mistimed check needs: a cost budgeted for
// August and paid in July is invisible from either month alone. Pass nil in
// year view, where "the wrong month" has no meaning.
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
			cents, ok := av.ByCategory[row.CategoryID]
			if ok {
				row.ActualCents = cents
				row.HasActual = true
				g.ActualCents += cents
				g.HasActual = true
			}
			row.ActualStatus, row.ActualNote = actualStatus(*row, av.Complete, viewed, charged)
			if row.ActualStatus == ActualMistimed {
				g.HasMistimed = true
			}
		}
	}
}

// actualStatus grades one row. The order of the checks is the ranking.
func actualStatus(row CategoryRow, coverageComplete bool, viewed time.Month, charged map[string][]time.Month) (status, note string) {
	if due, ok := plannedMonth(row.PlannedDate); ok && charged != nil {
		months := charged[row.CategoryID]
		// Charged here, but budgeted for another month.
		if row.HasActual && due != viewed {
			return ActualMistimed, fmt.Sprintf("planned for %s, charged now", due)
		}
		// Budgeted here and not charged here — but charged somewhere else.
		if !row.HasActual && due == viewed {
			if elsewhere, found := firstOtherMonth(months, viewed); found {
				return ActualMistimed, fmt.Sprintf("planned here, already charged in %s", elsewhere)
			}
		}
	}

	if !row.HasActual {
		return "", ""
	}
	switch {
	case row.PlannedCents == 0 && row.ActualCents > 0:
		return ActualUnbudgeted, ""
	case row.ActualCents > row.PlannedCents:
		return ActualOver, ""
	case row.ActualCents < row.PlannedCents && coverageComplete:
		return ActualUnder, ""
	}
	return "", ""
}

// plannedMonth extracts the month from a one-off's date. A recurring category
// has no date and can never be mistimed.
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
// panel-level summary.
func MistimedRowsOf(bv BudgetView) []MistimedRow {
	var out []MistimedRow
	out = append(out, mistimedIn(bv.Groups, false)...)
	out = append(out, mistimedIn(bv.CompanyGroups, true)...)
	return out
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

// UnmatchedCents is the recorded spending that reached no row at all: a
// category deleted from budget.json since the month was reconciled, or a
// dated one-off whose row is dropped once its month has passed. The whole
// point of actuals is that the figures reconcile, so this is shown rather
// than silently absorbed.
//
// companyIDs says which ids belong to company-kind groups, so the money lands
// in the ledger it came from. An id that isn't in budget.json at all has no
// kind to go on and falls to private — it's a data error the CLI catches
// hard, and one visible euro figure beats a silent one.
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
		if matched[id] {
			continue
		}
		if companyIDs[id] {
			company += cents
			continue
		}
		private += cents
	}
	return private, company
}

// LedgerCtx is what the categoryGroups template needs. It exists because a
// nested template's $ is its own argument, not the page data, so the
// page-level actuals flags have to travel with the groups.
type LedgerCtx struct {
	Groups            []CategoryGroupView
	ShowActuals       bool
	SpendingDetailURL string
}

// PrivateLedger and CompanyLedger pair each ledger's groups with the flags the
// template can't otherwise reach.
func (f Figures) PrivateLedger() LedgerCtx {
	return LedgerCtx{Groups: f.PrivateGroups, ShowActuals: f.ShowActuals, SpendingDetailURL: f.SpendingDetailURL}
}

func (f Figures) CompanyLedger() LedgerCtx {
	return LedgerCtx{Groups: f.FundingPersonal.CompanyGroups, ShowActuals: f.ShowActuals, SpendingDetailURL: f.SpendingDetailURL}
}
