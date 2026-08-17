package tracker

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

type SpendingView struct {
	Header webui.Header
	Month  string

	Nav         MonthNav
	OverviewURL string
	SpendingURL string
	RefreshURL  string

	Present  bool
	Balances []AccountRow
	Coverage []CoverageRow

	Groups    []SpendingGroup
	Ignored   []SpendingTx
	Unmatched []SpendingTx

	Untracked      []SpendingTx
	UntrackedCents int
	UntrackedCount int

	Movements []SpendingMovementGroup

	TotalCents   int
	IgnoredCount int
	Err          string
}

// SpendingMovementGroup lists the lines that moved money between the owner
// and the company. It is on the page to be seen and reaches no figure on it —
// the same standing as Untracked and Not budget expenses.
type SpendingMovementGroup struct {
	Name         string
	Note         string
	Cents        int
	Transactions []SpendingTx
}

// movementNames are in the schema's own order, so the page does not reshuffle
// itself between two reads of the same file.
var movementNames = []struct {
	Movement actualsdata.Movement
	Name     string
	Note     string
}{
	{actualsdata.MovementSalaryTransfer, "Salary paid across", "The salary the cascade above already accounts for, leaving the company for your own account."},
	{actualsdata.MovementOwnerDraw, "Owner draw", "Money taken out of the company outside payroll."},
	{actualsdata.MovementDividendPayout, "Dividend paid out", "A distribution reaching your account."},
	{actualsdata.MovementOwnerContribution, "Paid into the company", "Money you put in, which the company then owes you."},
	{actualsdata.MovementCorporateTax, "Company profit tax paid", "Left the company for the state, so it settles nothing between you and it."},
	{actualsdata.MovementDividendTax, "Dividend tax paid", "Left the company for the state, so it settles nothing between you and it."},
}

func movementGroups(byMovement map[actualsdata.Movement][]SpendingTx) []SpendingMovementGroup {
	var out []SpendingMovementGroup
	for _, m := range movementNames {
		rows := byMovement[m.Movement]
		if len(rows) == 0 {
			continue
		}
		group := SpendingMovementGroup{Name: m.Name, Note: m.Note, Transactions: rows}
		for _, row := range rows {
			group.Cents += row.Cents
		}
		out = append(out, group)
	}
	return out
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
	Status       string
	Mistimed     bool
	Note         string
	Transactions []SpendingTx
}

type SpendingTx struct {
	ID          string
	Date        string
	ISODate     string
	Description string
	Account     string
	Cents       int
	Reason      string
	Category    string

	PartOf string
}

func (t *Tracker) ComputeSpending(ctx context.Context, year int, month time.Month) SpendingView {
	now := time.Now().In(t.Loc)
	start := time.Date(year, month, 1, 0, 0, 0, 0, t.Loc)
	v := SpendingView{
		Month:       fmt.Sprintf("%s %d", month.String(), year),
		Nav:         monthNav(now, start, t.startMonth(), spendingURL),
		OverviewURL: monthURL(year, month),
		SpendingURL: spendingURL(year, month),
		RefreshURL:  spendingURL(year, month) + "?refresh=1",
	}
	if snap, ok, serr := t.Accounts.Snapshot(ctx, yearMonth{year, month}); serr == nil && ok {
		v.Balances = snap.AccountRow
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
		v.Coverage = append(v.Coverage, CoverageRow{Account: c.Account, From: formatDay(c.From), To: formatDay(c.To), ImportedAt: formatDay(c.ImportedAt)})
	}
	av, err := t.Actuals.ForMonth(ctx, year, month)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	if untracked, uerr := t.Actuals.UntrackedMonths(ctx, year, t.Start); uerr == nil {
		markUntrackedMonths(v.Nav.Months, untracked)
	}

	byCategory := map[string][]SpendingTx{}
	byMovement := map[actualsdata.Movement][]SpendingTx{}
	for _, tx := range af.Transactions {
		parts := actualsdata.PartsOf(tx)
		split := len(tx.Splits) > 0
		for _, part := range parts {
			row := SpendingTx{
				ID: tx.Id, Date: formatDay(tx.Date), ISODate: tx.Date,
				Description: tx.Description,
				Account:     tx.Account, Cents: eurToCents(part.Amount),
			}
			if split {
				row.PartOf = formatEuro(eurToCents(tx.Amount))
			}
			if part.Untracked != "" {
				row.Reason = part.Untracked
				v.Untracked = append(v.Untracked, row)
				v.UntrackedCount++
				v.UntrackedCents += row.Cents
				continue
			}
			if part.Ignored != "" {
				row.Reason = part.Ignored
				// A marked line carries an ignored reason too, so without
				// this it would be listed twice — here and under Not budget
				// expenses — and counted twice with it.
				if part.Movement != "" {
					byMovement[part.Movement] = append(byMovement[part.Movement], row)
					continue
				}
				v.Ignored = append(v.Ignored, row)
				v.IgnoredCount++
				continue
			}
			if part.Category == "" {
				continue
			}
			v.TotalCents += row.Cents
			byCategory[part.Category] = append(byCategory[part.Category], row)
		}
	}

	v.Movements = movementGroups(byMovement)

	var bv BudgetView
	if t.Budget != nil {
		if built, berr := t.Budget.ForMonth(ctx, year, month, time.Now().In(t.locOrUTC())); berr == nil {
			bv = built
			charged, cerr := t.Actuals.ChargedMonths(ctx, year, t.Start)
			if cerr == nil {
				ApplyActuals(&bv, av, month, charged)
			}
		}
	}

	seen := map[string]bool{}
	v.Groups = append(v.Groups, spendingGroups(bv.Groups, false, byCategory, seen)...)
	v.Groups = append(v.Groups, spendingGroups(bv.CompanyGroups, true, byCategory, seen)...)

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
				continue
			}
			seen[row.CategoryID] = true
			sg.Categories = append(sg.Categories, SpendingCategory{
				ID:           row.CategoryID,
				Name:         row.Name,
				PlannedCents: row.PlannedCents,
				ActualCents:  row.ActualCents,
				VarianceCent: row.ActualCents - row.PlannedCents,
				Status:       row.ActualStatus,
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

func (t SpendingTx) ChangeRequest() string {
	if t.PartOf != "" {
		return fmt.Sprintf("Change ID %s (%s / %s / %.2f of %s) like this: ",
			t.ID, t.ISODate, t.Description, float64(t.Cents)/100, t.PartOf)
	}
	return fmt.Sprintf("Change ID %s (%s / %s / %.2f) like this: ",
		t.ID, t.ISODate, t.Description, float64(t.Cents)/100)
}
