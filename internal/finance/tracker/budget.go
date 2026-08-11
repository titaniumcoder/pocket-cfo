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

// Budget reads budget.json from FS and caches it until evicted, the same
// pattern as Toggl. FS is a real directory at runtime (os.DirFS over DATA_DIR)
// — hand-edited data is read fresh, never embedded the way schemas/*.json is.
//
// A flat monthly plan, not envelope budgeting and not actual tracking: no
// logged spending, no rollover. Every category is a name and an amount that
// recurs monthly; one with a date is a one-off counted only in that month.
type Budget struct {
	FS fs.FS // required, no embedded fallback; tests pass an fstest.MapFS

	mu        sync.Mutex
	cache     *budgetResult
	minimalOn bool
}

// ToggleMinimal flips minimal-budget mode and reports the resulting state. A
// single global in-memory flag: it stays on as you browse between months, is
// never persisted, and resets on restart. Only ForMonth honors it.
func (b *Budget) ToggleMinimal() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minimalOn = !b.minimalOn
	return b.minimalOn
}

// IsMinimal reports whether minimal-budget mode is currently on.
func (b *Budget) IsMinimal() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.minimalOn
}

type budgetResult struct {
	file budgetdata.BudgetFile
	err  error
}

func (b *Budget) fsys() fs.FS {
	return b.FS
}

// File returns the cached budget.json, fetching and validating it on first
// use.
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

// Evict drops the cached budget.json — cheap to refetch, and it might have
// changed — used by the existing Reload link.
func (b *Budget) Evict() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cache = nil
}

// CategoryRow is one category's figure for a period, in cents. Planned* are
// only set for a dated category whose month hasn't arrived yet; Overridden
// marks a figure that came from an override rather than amount/minimal_amount.
// See categoryRowFor.
type CategoryRow struct {
	Name         string
	SpentCents   int
	PlannedCents int    // configured amount, shown ahead of time
	PlannedMonth string // e.g. "September 2026"
	Note         string
	URL          string // when set, the note renders as a link
	Overridden   bool
}

type LoanRow struct {
	Name        string
	AmountCents int
}

// CategoryGroupView is one budget.json group's rows for a period.
type CategoryGroupView struct {
	Name       string
	Rows       []CategoryRow
	SpentCents int // sum of Rows' SpentCents
}

// BudgetView is what the dashboard renders, split by group kind. Private is
// personal spending, deducted from Net income for the Balance row. Company is
// business expense, deducted from Company income *before* the salary cascade
// (see PersonalParams.breakdown) since it never becomes salary at all.
type BudgetView struct {
	Groups          []CategoryGroupView
	TotalSpentCents int

	CompanyGroups          []CategoryGroupView
	CompanyTotalSpentCents int
}

// ForMonth builds the budget view for one calendar month. A dated category is
// graded against the *viewed* month, not real now: browsing month-by-month
// should show a one-off as upcoming until its due month, active in it, and gone
// after, regardless of today's date. Company and private are identical here;
// the split only changes month ranges in ForYear.
func (b *Budget) ForMonth(ctx context.Context, year int, month time.Month, now time.Time) (BudgetView, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return BudgetView{}, err
	}
	key := monthKey(year, month)
	minimal := b.IsMinimal()
	spendFor := func(c budgetdata.Category) (int, bool) {
		_, overridden := overrideFor(c, key)
		return categoryCents(c, key, minimal), overridden
	}
	viewed := time.Date(year, month, 1, 0, 0, 0, 0, now.Location())
	return buildBudgetView(bf, spendFor, spendFor, viewed, minimal), nil
}

// privateExpenseStartMonth is where ForYear's private-category range starts:
// now's month for the current year, since months already gone by don't count;
// January for any other year. Also used by fundingRangeForYear, so the shifted
// funding-income range tracks the same span ForYear uses for expenses — the two
// must never drift apart.
func privateExpenseStartMonth(year int, now time.Time) time.Month {
	if year == now.Year() {
		return now.Month()
	}
	return time.January
}

// ForYear sums each category across the year, with the month range differing
// by kind. Private counts only the year's remaining months (see
// privateExpenseStartMonth). Company always counts all twelve, regardless of
// now: it feeds the salary cascade, which projects a full year of company
// income, so the cost side has to span the same full year to stay consistent.
// now is real current time, not the viewed year.
func (b *Budget) ForYear(ctx context.Context, year int, now time.Time) (BudgetView, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return BudgetView{}, err
	}
	privateStart := privateExpenseStartMonth(year, now)
	// Overridden is meaningful for a single month, not a yearly aggregate.
	privateSpend := func(c budgetdata.Category) (int, bool) {
		sum := 0
		for m := privateStart; m <= time.December; m++ {
			sum += categoryCents(c, monthKey(year, m), false)
		}
		return sum, false
	}
	companySpend := func(c budgetdata.Category) (int, bool) {
		sum := 0
		for m := time.January; m <= time.December; m++ {
			sum += categoryCents(c, monthKey(year, m), false)
		}
		return sum, false
	}
	return buildBudgetView(bf, privateSpend, companySpend, now, false), nil
}

// CompanyExpensesByMonth returns each month's company-kind spend for the year,
// all twelve unconditionally (see ForYear). Year view needs the per-month
// breakdown for the monthly social-insurance cap (PersonalParams.
// breakdownMonths); ForYear's aggregate isn't enough on its own.
func (b *Budget) CompanyExpensesByMonth(ctx context.Context, year int) (map[time.Month]int, error) {
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

// buildBudgetView computes each category's figure, using privateSpend or
// companySpend by group kind. ref and minimal are forwarded to categoryRowFor;
// ForYear always passes minimal=false, since year view ignores the toggle.
func buildBudgetView(bf budgetdata.BudgetFile, privateSpend, companySpend func(budgetdata.Category) (int, bool), ref time.Time, minimal bool) BudgetView {
	var view BudgetView
	for _, g := range bf.Groups {
		spendFor := privateSpend
		isCompany := g.Kind == budgetdata.GroupKindCompany
		if isCompany {
			spendFor = companySpend
		}
		gv := CategoryGroupView{Name: g.Name}
		for _, c := range g.Categories {
			spentCents, overridden := spendFor(c)
			row, ok := categoryRowFor(c, spentCents, overridden, ref, minimal)
			if !ok {
				continue
			}
			gv.Rows = append(gv.Rows, row)
			gv.SpentCents += row.SpentCents
		}
		if len(gv.Rows) == 0 {
			// Deliberately not "SpentCents == 0": a group of future-planned
			// one-offs also sums to zero but still has rows to render.
			continue
		}
		if isCompany {
			view.CompanyGroups = append(view.CompanyGroups, gv)
			view.CompanyTotalSpentCents += gv.SpentCents
		} else {
			view.Groups = append(view.Groups, gv)
			view.TotalSpentCents += gv.SpentCents
		}
	}
	return view
}

// categoryCents is a category's contribution to one month: its amount every
// month when it has no date, or only in its date's month when it has one.
func categoryCents(c budgetdata.Category, key string, minimal bool) int {
	if c.Date == nil {
		return eurToCents(categoryAmount(c, key, minimal))
	}
	d, err := time.Parse("2006-01-02", *c.Date)
	if err != nil {
		// Shouldn't happen — ValidateBudget already enforces the format.
		return 0
	}
	if monthKey(d.Year(), d.Month()) == key {
		return eurToCents(categoryAmount(c, key, minimal))
	}
	return 0
}

// overrideFor reports c's override amount for a "YYYY-MM" key, if any.
func overrideFor(c budgetdata.Category, key string) (float64, bool) {
	for _, ov := range c.Overrides {
		d, err := time.Parse("2006-01-02", ov.Month)
		if err != nil {
			continue // shouldn't happen — ValidateBudget already enforces the format
		}
		if monthKey(d.Year(), d.Month()) == key {
			return ov.Amount, true
		}
	}
	return 0, false
}

// categoryAmount is c's euro amount for a "YYYY-MM" key. An override wins
// unconditionally, including 0, even over minimal mode; then minimal_amount if
// minimal mode is on and one is set; then the normal amount.
func categoryAmount(c budgetdata.Category, key string, minimal bool) float64 {
	if amt, ok := overrideFor(c, key); ok {
		return amt
	}
	if minimal && c.MinimalAmount != nil {
		return *c.MinimalAmount
	}
	return c.Amount
}

// nextNonZeroMonthLookahead guarantees termination against a pathologically
// long Overrides list.
const nextNonZeroMonthLookahead = 24

// nextNonZeroMonth scans forward from ref for the next month c's amount would
// be non-zero, so a category zeroed out by an override can preview when it
// resumes rather than showing a bare 0.
func nextNonZeroMonth(c budgetdata.Category, ref time.Time, minimal bool) (time.Time, bool) {
	for i := 1; i <= nextNonZeroMonthLookahead; i++ {
		d := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location()).AddDate(0, i, 0)
		if categoryAmount(c, monthKey(d.Year(), d.Month()), minimal) > 0 {
			return d, true
		}
	}
	return time.Time{}, false
}

// categoryRowFor decides whether and how a category renders, given its
// already-computed spentCents.
func categoryRowFor(c budgetdata.Category, spentCents int, overridden bool, ref time.Time, minimal bool) (CategoryRow, bool) {
	row := baseCategoryRow(c)

	if spentCents == 0 && c.Date == nil && overridden {
		if preview, ok := zeroedRecurringPreview(c, row, ref, minimal); ok {
			return preview, true
		}
	}
	if spentCents > 0 || c.Date == nil {
		return normalRow(row, spentCents, overridden), true
	}
	return datedCategoryRow(c, row, spentCents, overridden, ref, minimal)
}

// baseCategoryRow fills in the fields every branch below shares.
func baseCategoryRow(c budgetdata.Category) CategoryRow {
	row := CategoryRow{Name: c.Name}
	if c.Note != nil {
		row.Note = *c.Note
	}
	if c.Url != nil {
		row.URL = *c.Url
	}
	return row
}

// zeroedRecurringPreview handles a recurring category zeroed out for the viewed
// month. Its normal amount is always > 0 (ValidateBudget enforces it), so a
// zero can only come from an override; rather than a bare 0, preview when the
// cost resumes. ok=false when nothing is non-zero within the lookahead, and the
// caller falls through to the normal render.
func zeroedRecurringPreview(c budgetdata.Category, row CategoryRow, ref time.Time, minimal bool) (CategoryRow, bool) {
	next, ok := nextNonZeroMonth(c, ref, minimal)
	if !ok {
		return CategoryRow{}, false
	}
	nextKey := monthKey(next.Year(), next.Month())
	row.PlannedCents = eurToCents(categoryAmount(c, nextKey, minimal))
	_, row.Overridden = overrideFor(c, nextKey)
	row.PlannedMonth = next.Format("January 2006")
	return row, true
}

// normalRow renders the common case: a recurring category, or a dated one due
// inside the viewed period.
func normalRow(row CategoryRow, spentCents int, overridden bool) CategoryRow {
	row.SpentCents = spentCents
	row.Overridden = overridden
	return row
}

// datedCategoryRow handles a dated category outside the viewed period: hidden
// once its month is before ref, previewed grayed-out while it is after.
//
// ref differs by caller. ForMonth passes the viewed month, so browsing shows a
// one-off as upcoming until its due month regardless of today's date; ForYear
// passes real now, since its own remaining-months logic already depends on it.
func datedCategoryRow(c budgetdata.Category, row CategoryRow, spentCents int, overridden bool, ref time.Time, minimal bool) (CategoryRow, bool) {
	d, err := time.Parse("2006-01-02", *c.Date)
	if err != nil {
		// ValidateBudget enforces the format, but don't hide a category over
		// a parse error; show it plainly.
		row.SpentCents = spentCents
		row.Overridden = overridden
		return row, true
	}
	if d.Year() > ref.Year() || (d.Year() == ref.Year() && d.Month() > ref.Month()) {
		dueKey := monthKey(d.Year(), d.Month())
		row.PlannedCents = eurToCents(categoryAmount(c, dueKey, minimal))
		_, row.Overridden = overrideFor(c, dueKey)
		row.PlannedMonth = d.Format("January 2006")
		return row, true
	}
	return CategoryRow{}, false
}

func eurToCents(euros float64) int { return int(math.Round(euros * 100)) }
