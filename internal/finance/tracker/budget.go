package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"sync"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
)

const budgetPath = "budget.json"

func monthKey(year int, month time.Month) string {
	return fmt.Sprintf("%04d-%02d", year, int(month))
}

type Budget struct {
	FS fs.FS

	mu        sync.Mutex
	cache     *budgetResult
	justWrote committed
}

func (b *Budget) Publish(body []byte) {
	if b == nil {
		return
	}
	b.justWrote.publish(budgetPath, body)
	b.Evict()
}

type budgetResult struct {
	file budgetdata.BudgetFile
	err  error
}

func (b *Budget) fsys() fs.FS {
	return b.FS
}

func (b *Budget) File(ctx context.Context) (budgetdata.BudgetFile, error) {
	b.mu.Lock()
	if b.cache != nil {
		cached := b.cache
		b.mu.Unlock()
		return cached.file, cached.err
	}
	b.mu.Unlock()

	log.Printf("budget: %s — fetching…", budgetPath)
	t0 := time.Now()
	bf, err := b.fetch()
	elapsed := time.Since(t0).Round(time.Millisecond)
	if err != nil {
		log.Printf("budget: %s — failed after %s: %v", budgetPath, elapsed, err)
	} else {
		log.Printf("budget: %s — fetched in %s", budgetPath, elapsed)
	}

	b.mu.Lock()
	b.cache = &budgetResult{file: bf, err: err}
	b.mu.Unlock()
	return bf, err
}

func (b *Budget) fetch() (budgetdata.BudgetFile, error) {
	content, err := fs.ReadFile(b.fsys(), budgetPath)
	if body, ok := b.justWrote.supersedes(budgetPath, content, err); ok {
		content, err = body, nil
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return budgetdata.BudgetFile{}, fmt.Errorf("budget: %s not found", budgetPath)
		}
		return budgetdata.BudgetFile{}, fmt.Errorf("budget: reading %s: %w", budgetPath, err)
	}
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(content, &bf); err != nil {
		return budgetdata.BudgetFile{}, fmt.Errorf("budget: parse %s: %w", budgetPath, err)
	}
	if err := budgetdata.ValidateBudget(bf); err != nil {
		return budgetdata.BudgetFile{}, fmt.Errorf("budget: %s: %w", budgetPath, err)
	}
	return bf, nil
}

func (b *Budget) Evict() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cache = nil
}

type CategoryRow struct {
	Name          string
	CategoryID    string
	PlannedDate   string
	PlannedCents  int
	UpcomingCents int
	UpcomingMonth string
	Note          string
	URL           string
	Overridden    bool

	// ScheduledChange* carry the category's nearest future amount change,
	// so the page can point at the month the price moves rather than making
	// the reader diff budget.json. Empty when no change is due within the
	// same horizon the upcoming-estimate previews use.
	ScheduledChangeURL     string
	ScheduledChangeTooltip string

	ActualCents  int
	HasActual    bool
	ActualStatus string
	ActualNote   string
}

type LoanRow struct {
	Name        string
	AmountCents int
}

type CategoryGroupView struct {
	Name         string
	Rows         []CategoryRow
	PlannedCents int

	ActualCents int
	HasActual   bool
	HasMistimed bool

	Status string
}

type BudgetView struct {
	Groups            []CategoryGroupView
	TotalPlannedCents int

	CompanyGroups            []CategoryGroupView
	CompanyTotalPlannedCents int

	// Dividends is the whole dated list, not the viewed month's share of it:
	// the roll-forward walks other months with this same view and each one
	// picks its own out, the way the salary plan and the target balance are
	// already read by month rather than filtered by the caller.
	Dividends Dividends
}

func (b *Budget) ForMonth(ctx context.Context, year int, month time.Month, now time.Time, minimal bool) (BudgetView, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return BudgetView{}, err
	}
	key := monthKey(year, month)
	plannedFor := func(c budgetdata.Category) (int, bool) {
		_, overridden := overrideFor(c, key)
		return categoryCents(c, key, minimal), overridden
	}
	viewed := time.Date(year, month, 1, 0, 0, 0, 0, now.Location())
	return buildBudgetView(bf, plannedFor, plannedFor, viewed, minimal), nil
}

func privateExpenseStartMonth(year int, now time.Time, floor yearMonth) time.Month {
	if year == now.Year() {
		if floor.Year == year && floor.Month > now.Month() {
			return floor.Month
		}
		return now.Month()
	}
	if floor.Year == year {
		return floor.Month
	}
	return time.January
}

func floorOf(start time.Time) yearMonth {
	if start.IsZero() {
		return yearMonth{}
	}
	return yearMonth{start.Year(), start.Month()}
}

func yearMonthRange(year int, floor yearMonth) (first, last time.Month) {
	first, last = time.January, time.December
	if floor.Year == year {
		first = floor.Month
	}
	return first, last
}

func (b *Budget) ForYear(ctx context.Context, year int, now, start time.Time) (BudgetView, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return BudgetView{}, err
	}
	privateStart := privateExpenseStartMonth(year, now, floorOf(start))
	privatePlanned := func(c budgetdata.Category) (int, bool) {
		sum := 0
		for m := privateStart; m <= time.December; m++ {
			sum += categoryCents(c, monthKey(year, m), false)
		}
		return sum, false
	}
	companyFirst, companyLast := yearMonthRange(year, floorOf(start))
	companyPlanned := func(c budgetdata.Category) (int, bool) {
		sum := 0
		for m := companyFirst; m <= companyLast; m++ {
			sum += categoryCents(c, monthKey(year, m), false)
		}
		return sum, false
	}
	return buildBudgetView(bf, privatePlanned, companyPlanned, now, false), nil
}

func (b *Budget) CompanyExpensesByMonth(ctx context.Context, year int, start time.Time) (map[time.Month]int, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return nil, err
	}
	result := map[time.Month]int{}
	for _, g := range bf.Groups {
		if g.Kind != budgetdata.GroupKindCompany {
			continue
		}
		for _, c := range g.Categories {
			for m := time.January; m <= time.December; m++ {
				result[m] += categoryCents(c, monthKey(year, m), false)
			}
		}
	}
	return result, nil
}

func buildBudgetView(bf budgetdata.BudgetFile, privatePlanned, companyPlanned func(budgetdata.Category) (int, bool), ref time.Time, minimal bool) BudgetView {
	view := BudgetView{Dividends: dividendsIn(bf)}
	for _, g := range bf.Groups {
		plannedFor := privatePlanned
		isCompany := g.Kind == budgetdata.GroupKindCompany
		if isCompany {
			plannedFor = companyPlanned
		}
		gv := CategoryGroupView{Name: g.Name}
		for _, c := range g.Categories {
			plannedCents, overridden := plannedFor(c)
			row, ok := categoryRowFor(c, plannedCents, overridden, ref, minimal)
			if !ok {
				continue
			}
			gv.Rows = append(gv.Rows, row)
			gv.PlannedCents += row.PlannedCents
		}
		if len(gv.Rows) == 0 {
			continue
		}
		if isCompany {
			view.CompanyGroups = append(view.CompanyGroups, gv)
			view.CompanyTotalPlannedCents += gv.PlannedCents
		} else {
			view.Groups = append(view.Groups, gv)
			view.TotalPlannedCents += gv.PlannedCents
		}
	}
	return view
}

func categoryCents(c budgetdata.Category, key string, minimal bool) int {
	if !categoryActiveIn(c, key) {
		return 0
	}
	return eurToCents(categoryAmount(c, key, minimal))
}

// categoryActiveIn reports whether a category counts toward month key: a
// one-off only in its own month, and a recurring cost whenever key falls
// inside its from/until window (either bound open-ended, so both absent
// means every month). The zero-padded YYYY-MM keys compare as strings.
func categoryActiveIn(c budgetdata.Category, key string) bool {
	if c.Date != nil {
		d, err := time.Parse("2006-01-02", *c.Date)
		if err != nil {
			return false
		}
		return monthKey(d.Year(), d.Month()) == key
	}
	if c.From != nil {
		d, err := time.Parse("2006-01-02", *c.From)
		if err == nil && monthKey(d.Year(), d.Month()) > key {
			return false
		}
	}
	if c.Until != nil {
		d, err := time.Parse("2006-01-02", *c.Until)
		if err == nil && monthKey(d.Year(), d.Month()) < key {
			return false
		}
	}
	return true
}

func overrideFor(c budgetdata.Category, key string) (float64, bool) {
	for _, ov := range c.Overrides {
		d, err := time.Parse("2006-01-02", ov.Month)
		if err != nil {
			continue
		}
		if monthKey(d.Year(), d.Month()) == key {
			return ov.Amount, true
		}
	}
	return 0, false
}

func categoryAmount(c budgetdata.Category, key string, minimal bool) float64 {
	if amt, ok := overrideFor(c, key); ok {
		return amt
	}
	amount, minimalAmount := c.Amount, c.MinimalAmount
	if ch, ok := amountChangeInForce(c, key); ok {
		amount, minimalAmount = ch.Amount, ch.MinimalAmount
	}
	if minimal && minimalAmount != nil {
		return *minimalAmount
	}
	return amount
}

// amountChangeInForce picks the latest amount_changes entry whose from month
// is at or before key, the way a dated legislation period is picked for a
// month. Entries are compared by their zero-padded YYYY-MM key, so string
// order is month order; validation already refused duplicates, so no tie is
// possible.
func amountChangeInForce(c budgetdata.Category, key string) (budgetdata.AmountChange, bool) {
	bestKey := ""
	var best budgetdata.AmountChange
	for _, ch := range c.AmountChanges {
		d, err := time.Parse("2006-01-02", ch.From)
		if err != nil {
			continue
		}
		fromKey := monthKey(d.Year(), d.Month())
		if fromKey <= key && fromKey > bestKey {
			best, bestKey = ch, fromKey
		}
	}
	return best, bestKey != ""
}

const nextNonZeroMonthLookahead = 24

func nextNonZeroMonth(c budgetdata.Category, ref time.Time, minimal bool) (time.Time, bool) {
	for i := 1; i <= nextNonZeroMonthLookahead; i++ {
		d := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location()).AddDate(0, i, 0)
		if c.Until != nil {
			until, err := time.Parse("2006-01-02", *c.Until)
			if err == nil && (d.Year() > until.Year() || (d.Year() == until.Year() && d.Month() > until.Month())) {
				return time.Time{}, false
			}
		}
		if categoryAmount(c, monthKey(d.Year(), d.Month()), minimal) > 0 {
			return d, true
		}
	}
	return time.Time{}, false
}

// nextAmountChange finds the earliest amount change whose month is strictly
// after ref — a change landing in the viewed month is already the price the
// row shows — within the same horizon the upcoming-estimate previews look
// ahead. Nothing beyond it is news the reader needs this visit.
func nextAmountChange(c budgetdata.Category, ref time.Time) (*budgetdata.AmountChange, time.Time) {
	base := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location())
	baseKey := monthKey(base.Year(), base.Month())
	horizon := base.AddDate(0, nextNonZeroMonthLookahead, 0)
	horizonKey := monthKey(horizon.Year(), horizon.Month())
	var bestKey string
	var best *budgetdata.AmountChange
	var bestWhen time.Time
	for i := range c.AmountChanges {
		d, err := time.Parse("2006-01-02", c.AmountChanges[i].From)
		if err != nil {
			continue
		}
		key := monthKey(d.Year(), d.Month())
		if key <= baseKey || key > horizonKey {
			continue
		}
		if best == nil || key < bestKey {
			best, bestKey, bestWhen = &c.AmountChanges[i], key, d
		}
	}
	return best, bestWhen
}

func categoryRowFor(c budgetdata.Category, plannedCents int, overridden bool, ref time.Time, minimal bool) (CategoryRow, bool) {
	row := baseCategoryRow(c)
	if ch, when := nextAmountChange(c, ref); ch != nil {
		// The tooltip names the price in the mode the page is showing: minimal
		// mode shows minimal figures, so a step with its own minimal_amount
		// speaks with that one here.
		price := ch.Amount
		if minimal && ch.MinimalAmount != nil {
			price = *ch.MinimalAmount
		}
		row.ScheduledChangeURL = monthURL(when.Year(), when.Month())
		row.ScheduledChangeTooltip = fmt.Sprintf("%s from %s", formatEuro(eurToCents(price)), when.Format("January 2006"))
	}

	if plannedCents == 0 && c.Date == nil && overridden {
		if preview, ok := zeroedRecurringPreview(c, row, ref, minimal); ok {
			return preview, true
		}
	}

	// A bounded recurring cost whose window is already over disappears, but
	// only when it contributes nothing here: month views pass the viewed month
	// as ref (an ended cost is 0 anyway), while year views pass now, so
	// plannedCents is the real in-window sum for that year — a company cost or
	// a past year sums all twelve months, and must not be rewritten out of its
	// own record.
	if c.Date == nil && c.Until != nil && plannedCents == 0 {
		until, err := time.Parse("2006-01-02", *c.Until)
		if err == nil && (until.Year() < ref.Year() || (until.Year() == ref.Year() && until.Month() < ref.Month())) {
			return CategoryRow{}, false
		}
	}

	if plannedCents > 0 || c.Date == nil {
		// A recurring cost that has not started yet is shown as an upcoming
		// estimate from its from month, the way a future one-off is.
		if c.Date == nil && c.From != nil && plannedCents == 0 {
			if d, err := time.Parse("2006-01-02", *c.From); err == nil && (d.Year() > ref.Year() || (d.Year() == ref.Year() && d.Month() > ref.Month())) {
				fromKey := monthKey(d.Year(), d.Month())
				row.UpcomingCents = eurToCents(categoryAmount(c, fromKey, minimal))
				_, row.Overridden = overrideFor(c, fromKey)
				row.UpcomingMonth = d.Format("January 2006")
				return row, true
			}
		}
		return normalRow(row, plannedCents, overridden), true
	}
	return datedCategoryRow(c, row, plannedCents, overridden, ref, minimal)
}

func baseCategoryRow(c budgetdata.Category) CategoryRow {
	row := CategoryRow{Name: c.Name, CategoryID: c.Id}
	if c.Date != nil {
		row.PlannedDate = *c.Date
	}
	if c.Note != nil {
		row.Note = *c.Note
	}
	if c.Url != nil {
		row.URL = *c.Url
	}
	return row
}

func zeroedRecurringPreview(c budgetdata.Category, row CategoryRow, ref time.Time, minimal bool) (CategoryRow, bool) {
	next, ok := nextNonZeroMonth(c, ref, minimal)
	if !ok {
		return CategoryRow{}, false
	}
	nextKey := monthKey(next.Year(), next.Month())
	row.UpcomingCents = eurToCents(categoryAmount(c, nextKey, minimal))
	_, row.Overridden = overrideFor(c, nextKey)
	row.UpcomingMonth = next.Format("January 2006")
	return row, true
}

func normalRow(row CategoryRow, plannedCents int, overridden bool) CategoryRow {
	row.PlannedCents = plannedCents
	row.Overridden = overridden
	return row
}

func datedCategoryRow(c budgetdata.Category, row CategoryRow, plannedCents int, overridden bool, ref time.Time, minimal bool) (CategoryRow, bool) {
	d, err := time.Parse("2006-01-02", *c.Date)
	if err != nil {
		row.PlannedCents = plannedCents
		row.Overridden = overridden
		return row, true
	}
	if d.Year() > ref.Year() || (d.Year() == ref.Year() && d.Month() > ref.Month()) {
		dueKey := monthKey(d.Year(), d.Month())
		row.UpcomingCents = eurToCents(categoryAmount(c, dueKey, minimal))
		_, row.Overridden = overrideFor(c, dueKey)
		row.UpcomingMonth = d.Format("January 2006")
		return row, true
	}
	return CategoryRow{}, false
}

func eurToCents(euros float64) int { return int(math.Round(euros * 100)) }
