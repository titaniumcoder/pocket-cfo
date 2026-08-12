package tracker

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

// SpendingView is the admin-only drill-down. It is the only place a statement
// description is rendered: Figures carries none, so a non-admin's HTML cannot
// contain one however the template is later edited.
type SpendingView struct {
	Header  webui.Header
	Month   string // "August 2026"
	BackURL string

	Present  bool
	Coverage []CoverageRow
	Note     string // the coverage caveat; empty once the period is fully read

	Groups    []SpendingGroup
	Ignored   []SpendingTx // not budget expenses; listed so the page reconciles to the statement
	Unmatched []SpendingTx // cites a category that no longer resolves

	TotalCents   int // every categorised line, matched or not
	IgnoredCount int
	Err          string
}

type CoverageRow struct {
	Account    string
	From       string
	To         string
	ImportedAt string
}

type SpendingGroup struct {
	Name       string
	Company    bool
	Categories []SpendingCategory
}

type SpendingCategory struct {
	ID           string
	Name         string
	PlannedCents int
	ActualCents  int
	VarianceCent int
	Mistimed     bool
	Note         string
	Transactions []SpendingTx
}

type SpendingTx struct {
	ID          string
	Date        string
	Description string
	Account     string
	Cents       int
	Reason      string // ignored lines only
	Category    string // unmatched lines only
}

// ComputeSpending assembles the detail page for one month. An unreconciled
// month returns Present=false rather than an error: it exists, it just has no
// data.
func (t *Tracker) ComputeSpending(ctx context.Context, year int, month time.Month) SpendingView {
	v := SpendingView{
		Month:   fmt.Sprintf("%s %d", month.String(), year),
		BackURL: fmt.Sprintf("/%d/%d", year, int(month)),
	}
	if t.Actuals == nil {
		return v
	}

	af, present, err := t.Actuals.TransactionsForMonth(ctx, year, month)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	if !present {
		return v
	}
	v.Present = true

	for _, c := range af.Coverage {
		v.Coverage = append(v.Coverage, CoverageRow{Account: c.Account, From: c.From, To: c.To, ImportedAt: c.ImportedAt})
	}
	av, err := t.Actuals.ForMonth(ctx, year, month)
	if err != nil {
		v.Err = err.Error()
		return v
	}

	byCategory := map[string][]SpendingTx{}
	for _, tx := range af.Transactions {
		row := SpendingTx{ID: tx.Id, Date: tx.Date, Description: tx.Description, Account: tx.Account, Cents: eurToCents(tx.Amount)}
		if tx.Ignored != nil && *tx.Ignored != "" {
			row.Reason = *tx.Ignored
			v.Ignored = append(v.Ignored, row)
			v.IgnoredCount++
			continue
		}
		if tx.Category == nil {
			continue
		}
		v.TotalCents += row.Cents
		byCategory[*tx.Category] = append(byCategory[*tx.Category], row)
	}

	// Reusing the dashboard's view keeps the two pages from disagreeing.
	var bv BudgetView
	if t.Budget != nil {
		if built, berr := t.Budget.ForMonth(ctx, year, month, time.Now().In(t.locOrUTC())); berr == nil {
			bv = built
			charged, cerr := t.Actuals.ChargedMonths(ctx, year)
			if cerr == nil {
				ApplyActuals(&bv, av, month, charged)
			}
		}
	}

	seen := map[string]bool{}
	v.Groups = append(v.Groups, spendingGroups(bv.Groups, false, byCategory, seen)...)
	v.Groups = append(v.Groups, spendingGroups(bv.CompanyGroups, true, byCategory, seen)...)

	// Anything left cites a category with no row this month.
	var leftover []string
	for id := range byCategory {
		if !seen[id] {
			leftover = append(leftover, id)
		}
	}
	sort.Strings(leftover)
	for _, id := range leftover {
		for _, tx := range byCategory[id] {
			tx.Category = id
			v.Unmatched = append(v.Unmatched, tx)
		}
	}
	return v
}

func (t *Tracker) locOrUTC() *time.Location {
	if t.Loc != nil {
		return t.Loc
	}
	return time.UTC
}

func spendingGroups(groups []CategoryGroupView, company bool, byCategory map[string][]SpendingTx, seen map[string]bool) []SpendingGroup {
	var out []SpendingGroup
	for _, g := range groups {
		sg := SpendingGroup{Name: g.Name, Company: company}
		for _, row := range g.Rows {
			txs := byCategory[row.CategoryID]
			if len(txs) == 0 && !row.HasActual && row.ActualStatus != ActualMistimed {
				continue // nothing recorded and nothing to explain
			}
			seen[row.CategoryID] = true
			sg.Categories = append(sg.Categories, SpendingCategory{
				ID:           row.CategoryID,
				Name:         row.Name,
				PlannedCents: row.PlannedCents,
				ActualCents:  row.ActualCents,
				VarianceCent: row.ActualCents - row.PlannedCents,
				Mistimed:     row.ActualStatus == ActualMistimed,
				Note:         row.ActualNote,
				Transactions: txs,
			})
		}
		if len(sg.Categories) > 0 {
			out = append(out, sg)
		}
	}
	return out
}

// ChangeRequest is what the spending page's copy link puts on the clipboard —
// enough for Hermes to identify the transaction, with the change itself left
// for the user to type.
func (t SpendingTx) ChangeRequest() string {
	// The exact amount, not formatEuro's whole euros: this identifies one
	// statement line, and 210 instead of 210.40 could match the wrong one.
	return fmt.Sprintf("Change ID %s (%s / %s / %.2f) like this: ",
		t.ID, t.Date, t.Description, float64(t.Cents)/100)
}
