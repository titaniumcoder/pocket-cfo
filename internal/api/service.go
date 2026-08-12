package api

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// Service answers Hermes' reads from the same cached loaders the dashboard
// uses, so the two can't disagree. It never sees an *http.Request, a session
// or a cookie: authentication lives entirely in the adapters.
type Service struct {
	Budget   *tracker.Budget
	Accounts *tracker.Accounts
	Actuals  *tracker.Actuals

	// Store is the git side of a write; nil means writes are not configured.
	// ActualsPrefix defaults to DefaultActualsPrefix.
	Store         Store
	ActualsPrefix string
	BudgetPath    string

	// Now is injected by tests; nil means time.Now.
	Now func() time.Time

	// Start is the first month budgeting covers, zero for none. The tracker
	// uses it to bound a year's aggregation; the reconciliation report follows,
	// so it does not list months the app itself refuses to show.
	Start time.Time
}

var monthRE = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ParseMonth accepts "2026-08".
func ParseMonth(s string) (int, time.Month, error) {
	if !monthRE.MatchString(s) {
		return 0, 0, errorf(CodeInvalidRequest, "month %q must look like 2026-08", s)
	}
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return 0, 0, errorf(CodeInvalidRequest, "month %q is not a real month", s)
	}
	return t.Year(), t.Month(), nil
}

// Category is one budget category, as the only legal value of a transaction's
// `category` field.
type Category struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Date  string `json:"date,omitempty"`
}

// Categories lists every category id a transaction may cite.
func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	idx, err := s.Budget.CategoryIndex(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	out := make([]Category, 0, len(idx))
	for _, c := range idx {
		out = append(out, Category{ID: c.ID, Group: c.Group, Name: c.Name, Kind: c.Kind, Date: c.Date})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// PlannedCategory is a category with its figure for one month.
type PlannedCategory struct {
	Category
	PlannedCents int  `json:"planned_cents"`
	Overridden   bool `json:"overridden"`
}

// MonthBudget is the plan for one month.
type MonthBudget struct {
	Month             string            `json:"month"`
	Categories        []PlannedCategory `json:"categories"`
	TotalPrivateCents int               `json:"total_private_cents"`
	TotalCompanyCents int               `json:"total_company_cents"`
}

// BudgetForMonth returns the plan for one month, overrides applied.
func (s *Service) BudgetForMonth(ctx context.Context, month string) (*MonthBudget, error) {
	year, m, err := ParseMonth(month)
	if err != nil {
		return nil, err
	}
	planned, err := s.Budget.PlannedForMonth(ctx, year, m)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	return monthBudgetOf(month, planned), nil
}

// BudgetForYear returns twelve month buckets. Deliberately not a single
// year-wide total: Budget.ForYear's private range starts at today's month, so
// one figure would change from day to day with nothing to explain why.
func (s *Service) BudgetForYear(ctx context.Context, year string) ([]*MonthBudget, error) {
	y, err := strconv.Atoi(year)
	if err != nil || y < 1970 || y > 9999 {
		return nil, errorf(CodeInvalidRequest, "year %q must look like 2026", year)
	}
	byMonth, err := s.Budget.PlannedByMonth(ctx, y)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	out := make([]*MonthBudget, 0, 12)
	for m := time.January; m <= time.December; m++ {
		key := monthKey(y, m)
		out = append(out, monthBudgetOf(key, byMonth[key]))
	}
	return out, nil
}

func monthBudgetOf(month string, planned []tracker.PlannedCategory) *MonthBudget {
	mb := &MonthBudget{Month: month, Categories: make([]PlannedCategory, 0, len(planned))}
	for _, c := range planned {
		mb.Categories = append(mb.Categories, PlannedCategory{
			Category:     Category{ID: c.ID, Group: c.Group, Name: c.Name, Kind: c.Kind, Date: c.Date},
			PlannedCents: c.PlannedCents,
			Overridden:   c.Overridden,
		})
		if c.Kind == "company" {
			mb.TotalCompanyCents += c.PlannedCents
			continue
		}
		mb.TotalPrivateCents += c.PlannedCents
	}
	sort.Slice(mb.Categories, func(i, j int) bool { return mb.Categories[i].ID < mb.Categories[j].ID })
	return mb
}

// Account is one account name, so a transaction spells it the way the rest of
// the system does.
type Account struct {
	Name string `json:"name"`
	AsOf string `json:"as_of,omitempty"`
}

// Accounts lists the known account names.
func (s *Service) AccountsList(ctx context.Context) ([]Account, error) {
	af, err := s.Accounts.File(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "reading accounts.json: %v", err)
	}
	out := make([]Account, 0, len(af.Accounts))
	for _, a := range af.Accounts {
		out = append(out, Account{Name: a.Name, AsOf: a.AsOf})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ActualsMonth is one committed month document.
type ActualsMonth struct {
	Month        string                    `json:"month"`
	Coverage     []actualsdata.Coverage    `json:"coverage"`
	Transactions []actualsdata.Transaction `json:"transactions"`

	// SHA is what a later write passes back as base_sha. Empty when writes
	// aren't configured, since there is then nothing to pass it to.
	SHA string `json:"sha,omitempty"`
}

// ActualsFor returns the committed document for a month, or not_found.
//
// With writes configured it reads through the store rather than DATA_DIR, so
// the document and the sha describe the same commit. The mounted checkout
// lags by a deploy: serving its bytes with a fresh sha would let a merge be
// built on a document that is no longer what the sha names, and the write
// would then be accepted.
func (s *Service) ActualsFor(ctx context.Context, month string) (*ActualsMonth, error) {
	year, m, err := ParseMonth(month)
	if err != nil {
		return nil, err
	}
	if s.Store != nil {
		return s.actualsFromStore(ctx, month)
	}
	af, present, ferr := s.Actuals.TransactionsForMonth(ctx, year, m)
	if ferr != nil {
		return nil, errorf(CodeInternal, "reading %s: %v", month, ferr)
	}
	if !present {
		return nil, errorf(CodeNotFound, "%s has not been reconciled", month)
	}
	return &ActualsMonth{Month: af.Month, Coverage: af.Coverage, Transactions: af.Transactions}, nil
}

func (s *Service) actualsFromStore(ctx context.Context, month string) (*ActualsMonth, error) {
	path := s.actualsPath(month)
	content, sha, err := s.Store.Get(ctx, path)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, errorf(CodeNotFound, "%s has not been reconciled", month)
		}
		if e, ok := err.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "reading %s: %v", path, err)
	}
	var af actualsdata.ActualsFile
	if err := json.Unmarshal(content, &af); err != nil {
		return nil, errorf(CodeUpstream, "%s does not parse: %v", path, err)
	}
	return &ActualsMonth{Month: af.Month, Coverage: af.Coverage, Transactions: af.Transactions, SHA: sha}, nil
}

// The year range every endpoint accepts. Wide enough never to be the reason
// a real request fails, narrow enough that a typo is caught as one.
const (
	minYear = 1970
	maxYear = 9999
)

// FoundTransaction is one committed transaction, with the month it came from.
type FoundTransaction struct {
	Month       string  `json:"month"`
	ID          string  `json:"id"`
	Date        string  `json:"date"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Account     string  `json:"account"`
	Category    string  `json:"category,omitempty"`
	Ignored     string  `json:"ignored,omitempty"`
	Untracked   string  `json:"untracked,omitempty"`

	// Splits is set instead of Category/Ignored/Untracked when the line paid
	// for more than one thing. The parts add up to Amount.
	Splits []FoundSplit `json:"splits,omitempty"`
}

// FoundSplit is one part of a split statement line.
type FoundSplit struct {
	Amount    float64 `json:"amount"`
	Category  string  `json:"category,omitempty"`
	Ignored   string  `json:"ignored,omitempty"`
	Untracked string  `json:"untracked,omitempty"`
}

func anyPart(parts []actualsdata.Part, match func(actualsdata.Part) bool) bool {
	for _, p := range parts {
		if match(p) {
			return true
		}
	}
	return false
}

// SearchQuery narrows a transaction search. An empty Query matches everything.
type SearchQuery struct {
	Query          string
	From           string // "2026-01", inclusive
	To             string // "2026-12", inclusive
	Category       string
	Account        string
	IncludeIgnored bool
	// OnlyUntracked narrows to lines still waiting on a decision, which is
	// how Hermes finds the cash it parked last month without reading twelve
	// files. Untracked lines are in the results by default either way —
	// unlike ignored ones, the whole point of untracked is being found again.
	OnlyUntracked bool
	Limit         int
	Years         []int // which years to scan; defaults to the current one
}

// SearchResult carries the matches plus whether the limit truncated them.
type SearchResult struct {
	Transactions []FoundTransaction `json:"transactions"`
	Truncated    bool               `json:"truncated"`
}

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 500
)

// Search is what replaces a rules file: Hermes asks whether it has seen a
// description before and gets the answer from committed history rather than
// from its own memory. Newest month first.
func (s *Service) Search(ctx context.Context, q SearchQuery) (*SearchResult, error) {
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultSearchLimit
	case limit > maxSearchLimit:
		limit = maxSearchLimit
	}

	months, err := s.monthsToScan(ctx, q)
	if err != nil {
		return nil, err
	}

	needle := strings.ToLower(q.Query)
	out := &SearchResult{Transactions: []FoundTransaction{}}
	for _, mk := range months {
		year, m, perr := ParseMonth(mk)
		if perr != nil {
			continue
		}
		af, present, ferr := s.Actuals.TransactionsForMonth(ctx, year, m)
		if ferr != nil {
			return nil, errorf(CodeInternal, "reading %s: %v", mk, ferr)
		}
		if !present {
			continue
		}
		for _, tx := range af.Transactions {
			parts := actualsdata.PartsOf(tx)
			// A split line is one result carrying its parts, not one result
			// per part: Hermes searches to learn how a merchant was treated
			// last time, and "50 cleaning, 50 restaurant out of 100" is the
			// answer. Its filters therefore match if any part matches.
			if !q.IncludeIgnored && !anyPart(parts, func(p actualsdata.Part) bool { return p.Ignored == "" }) {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(tx.Description), needle) {
				continue
			}
			if q.Category != "" && !anyPart(parts, func(p actualsdata.Part) bool { return p.Category == q.Category }) {
				continue
			}
			if q.OnlyUntracked && !anyPart(parts, func(p actualsdata.Part) bool { return p.Untracked != "" }) {
				continue
			}
			if q.Account != "" && tx.Account != q.Account {
				continue
			}
			if len(out.Transactions) == limit {
				out.Truncated = true
				return out, nil
			}
			found := FoundTransaction{
				Month: af.Month, ID: tx.Id, Date: tx.Date, Description: tx.Description,
				Amount: tx.Amount, Account: tx.Account,
			}
			if len(tx.Splits) > 0 {
				for _, p := range parts {
					found.Splits = append(found.Splits, FoundSplit{
						Amount: p.Amount, Category: p.Category, Ignored: p.Ignored, Untracked: p.Untracked,
					})
				}
			} else {
				found.Category, found.Ignored, found.Untracked = parts[0].Category, parts[0].Ignored, parts[0].Untracked
			}
			out.Transactions = append(out.Transactions, found)
		}
	}
	return out, nil
}

// scanYears works out which years a search covers: the explicit list, else
// the span from/to describes, else the current year. This lives here rather
// than in an adapter because both adapters need it and only one of them had
// it — a search for "2025-03" through MCP scanned 2026 and found nothing,
// silently, while the same search over REST worked.
func scanYears(q SearchQuery, currentYear int) []int {
	if len(q.Years) > 0 {
		return q.Years
	}
	lo, hi := 0, 0
	for _, bound := range []string{q.From, q.To} {
		y, err := yearOf(bound)
		if err != nil {
			continue
		}
		if lo == 0 || y < lo {
			lo = y
		}
		if y > hi {
			hi = y
		}
	}
	if lo == 0 {
		return []int{currentYear}
	}
	// Filled in, so from=2024-01&to=2026-12 scans 2025 as well.
	out := make([]int, 0, hi-lo+1)
	for y := lo; y <= hi; y++ {
		out = append(out, y)
	}
	return out
}

func yearOf(bound string) (int, error) {
	if len(bound) < 4 {
		return 0, errorf(CodeInvalidRequest, "%q is not a month", bound)
	}
	y, err := strconv.Atoi(bound[:4])
	if err != nil || y < minYear || y > maxYear {
		return 0, errorf(CodeInvalidRequest, "%q is not a month", bound)
	}
	return y, nil
}

// monthsToScan returns the month keys to search, newest first, honouring
// from/to when given.
func (s *Service) monthsToScan(ctx context.Context, q SearchQuery) ([]string, error) {
	years := scanYears(q, s.now().Year())
	var keys []string
	for _, y := range years {
		for m := time.January; m <= time.December; m++ {
			keys = append(keys, monthKey(y, m))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	if q.From == "" && q.To == "" {
		return keys, nil
	}
	if q.From != "" && !monthRE.MatchString(q.From) {
		return nil, errorf(CodeInvalidRequest, "from %q must look like 2026-08", q.From)
	}
	if q.To != "" && !monthRE.MatchString(q.To) {
		return nil, errorf(CodeInvalidRequest, "to %q must look like 2026-08", q.To)
	}
	var out []string
	for _, k := range keys {
		if q.From != "" && k < q.From {
			continue
		}
		if q.To != "" && k > q.To {
			continue
		}
		out = append(out, k)
	}
	return out, nil
}

// MonthStatus tells Hermes where it left off.
type MonthStatus struct {
	Month            string                 `json:"month"`
	Present          bool                   `json:"present"`
	Coverage         []actualsdata.Coverage `json:"coverage,omitempty"`
	TransactionCount int                    `json:"transaction_count"`
	IgnoredCount     int                    `json:"ignored_count"`
	PlannedCents     int                    `json:"planned_cents"`
	ActualCents      int                    `json:"actual_cents"`
	Complete         bool                   `json:"complete"`
	Mistimed         []MistimedCharge       `json:"mistimed,omitempty"`

	// UntrackedCount and UntrackedCents are money that has left the account
	// and still belongs to no category. Reported here so Hermes can see which
	// months have unfinished business without opening each one, and so a
	// month never quietly counts as done while cash is still unattributed.
	// Never part of ActualCents.
	UntrackedCount int `json:"untracked_count"`
	UntrackedCents int `json:"untracked_cents"`
}

// MistimedCharge is a one-off charged in a month other than the one it is
// budgeted for — the same red flag the dashboard shows, so Hermes doesn't
// have to recompute it.
type MistimedCharge struct {
	CategoryID  string `json:"category_id"`
	Name        string `json:"name"`
	PlannedFor  string `json:"planned_for"` // "2026-10"
	ChargedIn   string `json:"charged_in"`  // "2026-08"
	AmountCents int    `json:"amount_cents"`
}

// Reconciliation reports each month of a year: coverage, counts, planned vs
// actual, and any mistimed charge.
func (s *Service) Reconciliation(ctx context.Context, year int) ([]MonthStatus, error) {
	if year < minYear || year > maxYear {
		return nil, errorf(CodeInvalidRequest, "year must look like 2026")
	}
	byMonth, err := s.Budget.PlannedByMonth(ctx, year)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	idx, err := s.Budget.CategoryIndex(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	charged, err := s.Actuals.ChargedMonths(ctx, year, s.Start)
	if err != nil {
		return nil, errorf(CodeInternal, "scanning actuals: %v", err)
	}

	out := make([]MonthStatus, 0, 12)
	for m := time.January; m <= time.December; m++ {
		key := monthKey(year, m)
		st := MonthStatus{Month: key}
		for _, c := range byMonth[key] {
			st.PlannedCents += c.PlannedCents
		}

		af, present, ferr := s.Actuals.TransactionsForMonth(ctx, year, m)
		if ferr != nil {
			return nil, errorf(CodeInternal, "reading %s: %v", key, ferr)
		}
		if present {
			st.Present = true
			st.Coverage = af.Coverage
			st.TransactionCount = len(af.Transactions)
			for _, tx := range af.Transactions {
				parts := actualsdata.PartsOf(tx)
				spent := 0
				for _, p := range parts {
					// Attributed money only. Testing for a category rather
					// than for the absence of an ignored reason is what keeps
					// untracked cash out of the total: it has been spent, but
					// against no category, so comparing it to a plan would
					// misstate the plan and the spending both.
					if p.Category != "" {
						spent += eurToCents(p.Amount)
					}
					if p.Untracked != "" {
						st.UntrackedCents += eurToCents(p.Amount)
					}
				}
				if anyPart(parts, func(p actualsdata.Part) bool { return p.Untracked != "" }) {
					st.UntrackedCount++
				}
				// Ignored counts lines, not parts, and a part-ignored split
				// is not an ignored line — its money is still in the total.
				if !anyPart(parts, func(p actualsdata.Part) bool { return p.Ignored == "" }) {
					st.IgnoredCount++
					continue
				}
				st.ActualCents += spent
			}
			view, verr := s.Actuals.ForMonth(ctx, year, m)
			if verr != nil {
				return nil, errorf(CodeInternal, "reading %s: %v", key, verr)
			}
			st.Complete = view.Complete
			st.Mistimed = mistimedIn(af, year, m, byMonth[key], idx, charged)
		}
		out = append(out, st)
	}
	return out, nil
}

// mistimedIn reports both directions of a mistimed one-off, matching what
// actualStatus renders on the dashboard: a charge that landed in a month the
// plan didn't expect, and a month the plan expects a charge in that has
// already been paid elsewhere. The second is invisible from this month's
// transactions alone — there are none to look at — which is what charged is
// for, and is the case that would otherwise look fine while the money is
// already gone.
//
// Like the dashboard, this only runs for a month that has been reconciled: an
// unread month says nothing either way rather than guessing.
func mistimedIn(af actualsdata.ActualsFile, year int, viewed time.Month, planned []tracker.PlannedCategory, idx map[string]tracker.PlannedCategory, charged map[string][]time.Month) []MistimedCharge {
	here := monthKey(year, viewed)
	var out []MistimedCharge
	seen := map[string]bool{}
	chargedHere := map[string]bool{}

	for _, tx := range af.Transactions {
		for _, part := range actualsdata.PartsOf(tx) {
			id := part.Category
			if id == "" {
				continue
			}
			chargedHere[id] = true
			if seen[id] {
				continue
			}
			cat, ok := idx[id]
			if !ok || cat.Date == "" {
				continue
			}
			due, err := dueMonth(cat.Date)
			// Compared as year-and-month: a one-off dated next August is not
			// the same plan as this August, and reading it as one hides a
			// real charge.
			if err != nil || due == here {
				continue
			}
			seen[id] = true
			out = append(out, MistimedCharge{
				CategoryID: id, Name: cat.Name,
				PlannedFor: due, ChargedIn: here,
				// The part's own amount: a laptop bought inside a 2 000 card
				// settlement is a 1 800 problem, not a 2 000 one.
				AmountCents: eurToCents(part.Amount),
			})
		}
	}

	for _, c := range planned {
		if c.Date == "" || seen[c.ID] || chargedHere[c.ID] {
			continue
		}
		due, err := dueMonth(c.Date)
		if err != nil || due != here {
			continue
		}
		elsewhere, found := firstOtherMonth(charged[c.ID], viewed)
		if !found {
			continue
		}
		seen[c.ID] = true
		out = append(out, MistimedCharge{
			CategoryID: c.ID, Name: c.Name,
			PlannedFor: here, ChargedIn: monthKey(year, elsewhere),
			// Nothing was charged here, so the plan's own figure is the
			// amount at stake — the same fallback MistimedRowsOf makes.
			AmountCents: c.PlannedCents,
		})
	}
	return out
}

// dueMonth renders a category's date as the month key it belongs to.
func dueMonth(date string) (string, error) {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", err
	}
	return monthKey(d.Year(), d.Month()), nil
}

func firstOtherMonth(months []time.Month, viewed time.Month) (time.Month, bool) {
	for _, m := range months {
		if m != viewed {
			return m, true
		}
	}
	return 0, false
}

func monthKey(year int, month time.Month) string {
	return strconv.Itoa(year) + "-" + twoDigit(int(month))
}

func twoDigit(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func eurToCents(euros float64) int {
	if euros < 0 {
		return -int(-euros*100 + 0.5)
	}
	return int(euros*100 + 0.5)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
