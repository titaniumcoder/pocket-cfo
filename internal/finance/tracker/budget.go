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

// Budget reads the whole budget (budget.json) from FS and caches it in
// memory, mirroring Toggl's cache-forever-until-evicted pattern (the
// existing Reload link, Tracker.EvictMonth/EvictYear, evicts it the same
// way it evicts Toggl data). FS is read fresh from disk at runtime — a real
// directory via os.DirFS (see cmd/pocketcfo's BUDGET_DIR wiring), same
// "hand-edited, read fresh, never embedded" convention as
// data/recipients, data/invoices, data/users.json — not baked into the
// binary at build time the way schemas/*.json is.
//
// This is a flat monthly plan, not envelope budgeting and not actual
// tracking — there's no logged spending, no override, no rollover. Every
// category is just a name and an amount that recurs every month; a category
// with a date is a one-off cost that only counts in that specific month.
type Budget struct {
	// FS is where budget.json is read from — required, no embedded
	// fallback. Tests pass an fstest.MapFS.
	FS fs.FS

	mu        sync.Mutex
	val       *budgetResult
	minimalOn bool // minimal-budget mode — see ToggleMinimal/IsMinimal
}

// ToggleMinimal flips minimal-budget mode and reports the resulting state.
// A single global, in-memory flag — not tied to any specific month (so it
// stays on as you browse from month to month) and never persisted to
// budget.json; it resets to off on process restart. Only ForMonth honors it
// — ForYear/CompanyExpensesByMonth always use full amounts.
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
	val budgetdata.BudgetFile
	err error
}

func (b *Budget) fsys() fs.FS {
	return b.FS
}

// File returns the cached budget.json, fetching and validating it on first
// use.
func (b *Budget) File(ctx context.Context) (budgetdata.BudgetFile, error) {
	b.mu.Lock()
	if b.val != nil {
		v := b.val
		b.mu.Unlock()
		return v.val, v.err
	}
	b.mu.Unlock()

	log.Printf("budget: %s — fetching…", budgetPath)
	t0 := time.Now()
	val, err := b.fetch()
	elapsed := time.Since(t0).Round(time.Millisecond)
	if err != nil {
		log.Printf("budget: %s — failed after %s: %v", budgetPath, elapsed, err)
	} else {
		log.Printf("budget: %s — fetched in %s", budgetPath, elapsed)
	}

	b.mu.Lock()
	b.val = &budgetResult{val: val, err: err}
	b.mu.Unlock()
	return val, err
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
	b.val = nil
}

// CategoryRow is one category's figure for a period, in cents.
// PlannedMonth and PlannedCents are only ever set for a dated category whose
// month hasn't arrived yet — see categoryRowFor. Note/URL are carried
// straight from budget.json for display — see categoryRowFor. Overridden
// marks that the shown figure (SpentCents, or PlannedCents for a future
// preview) came from an override rather than the category's normal
// amount/minimal_amount — see categoryAmount.
type CategoryRow struct {
	Name         string
	SpentCents   int
	PlannedCents int    // the category's configured amount, shown ahead of time
	PlannedMonth string // e.g. "September 2026"
	Note         string
	URL          string // opens in a new tab when set; empty means the note (if any) isn't a link
	Overridden   bool
}

// LoanRow is one loan's balance for display, in cents.
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

// BudgetView is what the dashboard renders, split by group kind (see
// budget.schema.json): Groups/TotalSpentCents are "private" — ordinary
// personal spending, shown in the Expenses section and deducted from Net
// income for the Balance row. CompanyGroups/CompanyTotalSpentCents are
// "company" — business expenses, shown under Personal income and deducted
// from Company income before the salary cascade runs (see
// PersonalParams.breakdown), since that money never becomes personal salary
// at all.
type BudgetView struct {
	Groups          []CategoryGroupView
	TotalSpentCents int

	CompanyGroups          []CategoryGroupView
	CompanyTotalSpentCents int
}

// ForMonth builds the budget view for one calendar month. A dated category
// not due this month is graded "future" or "past" (see categoryRowFor)
// against the *viewed* month itself, not real current time — browsing
// month-by-month should show a one-time cost as upcoming right up through
// the month before it's due, active in its due month, and gone from the
// month after, regardless of what today's real date happens to be. (Company
// and private categories are computed identically here — both use the
// single viewed month; the company/private split only changes month-range
// behavior in ForYear, see below, which still compares to real `now` for its
// own reasons.)
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

// privateExpenseStartMonth is the first month ForYear's private-category
// month range starts at for the given viewed year: the year "now" falls in
// only counts from now's month through December (a month already gone by
// doesn't count); any other year (past or future relative to now) counts
// from January, since none of it is "already gone by" relative to itself.
// Also reused by Tracker.fundingIncome's fundingRangeForYear, so the shifted
// funding-income range always tracks the exact same range ForYear uses for
// expenses — the two must never drift apart.
func privateExpenseStartMonth(year int, now time.Time) time.Month {
	if year == now.Year() {
		return now.Month()
	}
	return time.January
}

// ForYear sums each category's figure across the given year — the month
// range differs by group kind. Private categories only count the year's
// remaining months (for the year real "now" falls in, that's now's month
// through December; months already gone by don't count, same spirit as a
// past one-time category being hidden rather than shown in month view — see
// categoryRowFor); any other year (past or future) is entirely "remaining"
// relative to itself, so all twelve months count. Company categories always
// count all twelve months of the viewed year, regardless of "now" — they
// feed the salary cascade (see PersonalParams.breakdown), which projects a
// full year of company income (actual-so-far blended with expected-going-
// forward, see Tracker.compute), so the company-cost side of that
// calculation has to span the same full year to stay consistent, not just
// what's left of it. now is real current time (not the viewed year).
func (b *Budget) ForYear(ctx context.Context, year int, now time.Time) (BudgetView, error) {
	bf, err := b.File(ctx)
	if err != nil {
		return BudgetView{}, err
	}
	privateStart := privateExpenseStartMonth(year, now)
	// The Overridden marker is meaningful for a single viewed month, not a
	// yearly aggregate spanning several months' overrides — always false here.
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

// CompanyExpensesByMonth returns each calendar month's total company-kind
// category spend for the given year (all twelve months, unconditionally —
// see ForYear), keyed by month. Tracker.compute uses this to deduct company
// expenses from each month's company income before running the salary
// cascade for year view, where the per-month breakdown matters for the
// monthly social-insurance cap (see PersonalParams.breakdownMonths) — the
// aggregate CompanyTotalSpentCents from ForYear isn't enough on its own.
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

// buildBudgetView computes each category's figure for a period, using
// privateSpend or companySpend depending on the group's kind (see
// ForMonth/ForYear — the two coincide for ForMonth, and differ in month
// range for ForYear) against the current category definitions. ref is
// forwarded to categoryRowFor as-is — see there for what it means for each
// caller. minimal is forwarded to categoryRowFor for its future-planned
// preview amount — ForMonth passes the live minimal-budget flag, ForYear
// always passes false (year view is unaffected by minimal-budget mode).
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
			// Every category in the group was hidden by categoryRowFor (a
			// past one-time cost, nothing ever due) — nothing left to show.
			// This is deliberately not "SpentCents == 0": a group full of
			// future-planned one-time categories also sums to zero but still
			// has rows (the grayed-out "3,000 (September 2026)" reminders),
			// and those still need to render.
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

// categoryCents is a category's contribution to one specific month (key,
// "YYYY-MM"): its amount every month when it has no date (a recurring cost),
// or its amount only in the one month its date falls in (a one-off cost) —
// zero for every other month. minimal substitutes in the category's
// minimal_amount (when it has one) via categoryAmount, unless key has an
// override — see categoryAmount, which always wins, including an override of
// 0 (the replacement for what excluded_months used to do).
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

// overrideFor reports c's override amount for key (a "YYYY-MM" month key), if
// any — day is informational/ignored, same convention as date.
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

// categoryAmount is c's euro amount for key (a "YYYY-MM" month key): an
// override for that month always wins when present (even 0 — the
// replacement for what excluded_months used to do), unconditionally over
// minimal-budget mode; otherwise minimal_amount when minimal-budget mode is
// on and one is configured; otherwise the normal amount. A category with no
// minimal_amount (a fixed cost, e.g. Rent) always uses its normal amount
// regardless of minimal.
func categoryAmount(c budgetdata.Category, key string, minimal bool) float64 {
	if amt, ok := overrideFor(c, key); ok {
		return amt
	}
	if minimal && c.MinimalAmount != nil {
		return *c.MinimalAmount
	}
	return c.Amount
}

// nextNonZeroMonthLookahead caps how far nextNonZeroMonth scans forward, to
// guarantee termination even against a pathologically long Overrides list.
const nextNonZeroMonthLookahead = 24

// nextNonZeroMonth scans forward from ref (exclusive) for the next month
// c's amount would be non-zero — used when a recurring category's spend for
// the viewed month is entirely zeroed out by an override (e.g. "trip
// skipped this month"), so the preview can show when the cost resumes
// instead of leaving a bare 0 with no explanation. A recurring category's
// normal amount is always > 0 (ValidateBudget enforces this), so the scan
// is bounded by however many consecutive zero-overrides it has, plus one.
func nextNonZeroMonth(c budgetdata.Category, ref time.Time, minimal bool) (time.Time, bool) {
	for i := 1; i <= nextNonZeroMonthLookahead; i++ {
		d := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location()).AddDate(0, i, 0)
		if categoryAmount(c, monthKey(d.Year(), d.Month()), minimal) > 0 {
			return d, true
		}
	}
	return time.Time{}, false
}

// categoryRowFor decides whether/how a category renders as a CategoryRow,
// given its already-computed spentCents for the period. A recurring
// category, and any dated category whose due month falls inside the viewed
// period (spentCents > 0), always renders normally. A dated category whose
// month isn't in the viewed period instead depends on comparing its date to
// ref at month granularity: not shown at all once its month is before ref —
// dwelling on it is just clutter — shown with PlannedCents/PlannedMonth set
// (the template grays it out and shows its configured amount and month —
// "3,000 (September 2026)" — instead of a spent figure) while its month is
// after ref. ref means different things depending on the caller: ForMonth
// passes the viewed month itself (so browsing month-by-month shows a
// one-time cost as upcoming right up to its due month and gone the month
// after, regardless of today's real date); ForYear passes real current time
// (its own "remaining months" logic already depends on real now, so the
// per-row decision stays consistent with that rather than the viewed year).
// minimal substitutes in minimal_amount for the PlannedCents preview too —
// since the minimal-budget flag is global (not tied to a specific month),
// there's no ambiguity about which month's toggle state a future preview
// should reflect. overridden marks whether spentCents came from an override
// of the viewed month (see categoryAmount) — set on the row in the normal
// branch only; the future-preview branch looks up its own due-month override
// directly, since it isn't the viewed month. A recurring category (c.Date ==
// nil) can only ever be zero via a 0-amount override for the viewed month
// (its normal amount is always > 0, see ValidateBudget) — when that happens,
// this renders the same grayed-out PlannedCents/PlannedMonth preview as the
// dated-future branch below, pointed at nextNonZeroMonth, instead of a bare
// 0 with no indication of when the cost resumes.
func categoryRowFor(c budgetdata.Category, spentCents int, overridden bool, ref time.Time, minimal bool) (CategoryRow, bool) {
	row := CategoryRow{Name: c.Name}
	if c.Note != nil {
		row.Note = *c.Note
	}
	if c.Url != nil {
		row.URL = *c.Url
	}
	if spentCents == 0 && c.Date == nil && overridden {
		if next, ok := nextNonZeroMonth(c, ref, minimal); ok {
			nextKey := monthKey(next.Year(), next.Month())
			row.PlannedCents = eurToCents(categoryAmount(c, nextKey, minimal))
			_, row.Overridden = overrideFor(c, nextKey)
			row.PlannedMonth = next.Format("January 2006")
			return row, true
		}
	}
	if spentCents > 0 || c.Date == nil {
		row.SpentCents = spentCents
		row.Overridden = overridden
		return row, true
	}
	d, err := time.Parse("2006-01-02", *c.Date)
	if err != nil {
		// Shouldn't happen — ValidateBudget already enforces the format —
		// but don't hide a category over a parse error; show it plainly.
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
