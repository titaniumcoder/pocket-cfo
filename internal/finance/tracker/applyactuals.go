package tracker

import (
	"fmt"
	"time"
)

const (
	ActualMistimed   = "mistimed"
	ActualOver       = "over"
	ActualUnbudgeted = "unbudgeted"
	ActualUnder      = "under"
)

type MistimedRow struct {
	Name    string
	Note    string
	Cents   int
	Company bool
}

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

func onPlanTolerance(plannedCents int) int {
	const floor = 2000
	if pct := plannedCents / 20; pct > floor {
		return pct
	}
	return floor
}

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
