package api

import (
	"context"
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
}

// ActualsFor returns the committed document for a month, or not_found.
func (s *Service) ActualsFor(ctx context.Context, month string) (*ActualsMonth, error) {
	year, m, err := ParseMonth(month)
	if err != nil {
		return nil, err
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
}

// SearchQuery narrows a transaction search. An empty Query matches everything.
type SearchQuery struct {
	Query          string
	From           string // "2026-01", inclusive
	To             string // "2026-12", inclusive
	Category       string
	Account        string
	IncludeIgnored bool
	Limit          int
	Years          []int // which years to scan; defaults to the current one
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
			ignored := deref(tx.Ignored)
			if ignored != "" && !q.IncludeIgnored {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(tx.Description), needle) {
				continue
			}
			if q.Category != "" && deref(tx.Category) != q.Category {
				continue
			}
			if q.Account != "" && tx.Account != q.Account {
				continue
			}
			if len(out.Transactions) == limit {
				out.Truncated = true
				return out, nil
			}
			out.Transactions = append(out.Transactions, FoundTransaction{
				Month: af.Month, ID: tx.Id, Date: tx.Date, Description: tx.Description,
				Amount: tx.Amount, Account: tx.Account, Category: deref(tx.Category), Ignored: ignored,
			})
		}
	}
	return out, nil
}

// monthsToScan returns the month keys to search, newest first, honouring
// from/to when given.
func (s *Service) monthsToScan(ctx context.Context, q SearchQuery) ([]string, error) {
	years := q.Years
	if len(years) == 0 {
		years = []int{time.Now().Year()}
	}
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
}

// MistimedCharge is a one-off charged in a month other than the one it is
// budgeted for — the same red flag the dashboard shows, so Hermes doesn't
// have to recompute it.
type MistimedCharge struct {
	CategoryID  string `json:"category_id"`
	Name        string `json:"name"`
	PlannedFor  string `json:"planned_for"`
	ChargedIn   string `json:"charged_in"`
	AmountCents int    `json:"amount_cents"`
}

// Reconciliation reports each month of a year: coverage, counts, planned vs
// actual, and any mistimed charge.
func (s *Service) Reconciliation(ctx context.Context, year int) ([]MonthStatus, error) {
	byMonth, err := s.Budget.PlannedByMonth(ctx, year)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	idx, err := s.Budget.CategoryIndex(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	charged, err := s.Actuals.ChargedMonths(ctx, year)
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
				if deref(tx.Ignored) != "" {
					st.IgnoredCount++
					continue
				}
				st.ActualCents += eurToCents(tx.Amount)
			}
			view, verr := s.Actuals.ForMonth(ctx, year, m)
			if verr != nil {
				return nil, errorf(CodeInternal, "reading %s: %v", key, verr)
			}
			st.Complete = view.Complete
			st.Mistimed = mistimedIn(af, m, idx, charged)
		}
		out = append(out, st)
	}
	return out, nil
}

func mistimedIn(af actualsdata.ActualsFile, viewed time.Month, idx map[string]tracker.PlannedCategory, charged map[string][]time.Month) []MistimedCharge {
	var out []MistimedCharge
	seen := map[string]bool{}
	for _, tx := range af.Transactions {
		id := deref(tx.Category)
		if id == "" || seen[id] {
			continue
		}
		cat, ok := idx[id]
		if !ok || cat.Date == "" {
			continue
		}
		due, err := time.Parse("2006-01-02", cat.Date)
		if err != nil || due.Month() == viewed {
			continue
		}
		seen[id] = true
		out = append(out, MistimedCharge{
			CategoryID: id, Name: cat.Name,
			PlannedFor: due.Month().String(), ChargedIn: viewed.String(),
			AmountCents: eurToCents(tx.Amount),
		})
	}
	return out
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
