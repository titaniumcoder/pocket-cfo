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

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

type Service struct {
	Budget   *tracker.Budget
	Accounts *tracker.Accounts
	Actuals  *tracker.Actuals

	Store         Store
	ActualsPrefix string
	BudgetPath    string
	AccountsPath  string

	InvoicesDir      string
	InvoicesPrefix   string
	PaidInvoicesPath string
	CatalogPath      string
	ConfigPath       string

	Now func() time.Time

	Start time.Time

	// Personal is the dated configuration the finance figures are computed
	// from, reported read-only.
	Personal tracker.PersonalParams

	// Figures answers with the very computation the dashboard renders, so a
	// figure this API reports and a figure the page shows cannot drift.
	Figures func(ctx context.Context, year int, month time.Month) (tracker.Figures, error)
}

var monthRE = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

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

type Category struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Date  string `json:"date,omitempty"`
	// From/Until mark a recurring cost bounded to a window; either may be
	// empty. A category with Date is a one-off, never bounded.
	From  string `json:"from,omitempty"`
	Until string `json:"until,omitempty"`
	// AmountChanges carries the category's whole dated step list, so an agent
	// can see which future months are already spoken for before it schedules
	// another change. Empty for a category with a flat amount.
	AmountChanges []budgetdata.AmountChange `json:"amount_changes,omitempty"`
}

func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	idx, err := s.Budget.CategoryIndex(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	out := make([]Category, 0, len(idx))
	for _, c := range idx {
		out = append(out, Category{ID: c.ID, Group: c.Group, Name: c.Name, Kind: c.Kind, Date: c.Date, From: c.From, Until: c.Until, AmountChanges: c.AmountChanges})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type PlannedCategory struct {
	Category
	PlannedCents int  `json:"planned_cents"`
	Overridden   bool `json:"overridden"`
}

type MonthBudget struct {
	Month             string            `json:"month"`
	Categories        []PlannedCategory `json:"categories"`
	TotalPrivateCents int               `json:"total_private_cents"`
	TotalCompanyCents int               `json:"total_company_cents"`
	SHA               string            `json:"sha,omitempty"`
}

func (s *Service) BudgetForMonth(ctx context.Context, month string) (*MonthBudget, error) {
	year, m, err := ParseMonth(month)
	if err != nil {
		return nil, err
	}
	planned, err := s.Budget.PlannedForMonth(ctx, year, m)
	if err != nil {
		return nil, errorf(CodeInternal, "reading budget.json: %v", err)
	}
	mb := monthBudgetOf(month, planned)
	mb.SHA = s.budgetSHA(ctx)
	return mb, nil
}

func (s *Service) budgetPath() string {
	if s.BudgetPath != "" {
		return s.BudgetPath
	}
	return "data/budget.json"
}

func (s *Service) actualsPath(month string) string {
	prefix := s.ActualsPrefix
	if prefix == "" {
		prefix = "data/actuals"
	}
	return prefix + "/" + month + ".json"
}

func (s *Service) ActualsPath(month string) string { return s.actualsPath(month) }

func (s *Service) budgetSHA(ctx context.Context) string {
	if s.Store == nil {
		return ""
	}
	_, sha, err := s.Store.Get(ctx, s.budgetPath())
	if err != nil {
		return ""
	}
	return sha
}

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
			Category:     Category{ID: c.ID, Group: c.Group, Name: c.Name, Kind: c.Kind, Date: c.Date, From: c.From, Until: c.Until, AmountChanges: c.AmountChanges},
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

type Account struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	AsOf string `json:"as_of,omitempty"`
}

func (s *Service) AccountsList(ctx context.Context) ([]Account, error) {
	af, err := s.Accounts.File(ctx)
	if err != nil {
		return nil, errorf(CodeInternal, "reading accounts.json: %v", err)
	}
	out := make([]Account, 0, len(af.Accounts))
	for _, a := range af.Accounts {
		out = append(out, Account{Name: a.Name, Kind: string(a.Kind), AsOf: newestAsOf(a)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func newestAsOf(a accountsdata.Account) string {
	newest := ""
	for _, r := range a.Balances {
		if r.AsOf > newest {
			newest = r.AsOf
		}
	}
	return newest
}

type ActualsMonth struct {
	Month        string                    `json:"month"`
	Transactions []actualsdata.Transaction `json:"transactions"`

	SHA string `json:"sha,omitempty"`
}

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
	return &ActualsMonth{Month: af.Month, Transactions: af.Transactions}, nil
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
	return &ActualsMonth{Month: af.Month, Transactions: af.Transactions, SHA: sha}, nil
}

const (
	minYear = 1970
	maxYear = 9999
)

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

	Splits []FoundSplit `json:"splits,omitempty"`
}

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

type SearchQuery struct {
	Query          string
	From           string
	To             string
	Category       string
	Account        string
	IncludeIgnored bool
	OnlyUntracked  bool
	Limit          int
	Years          []int
}

type SearchResult struct {
	Transactions []FoundTransaction `json:"transactions"`
	Truncated    bool               `json:"truncated"`
}

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 500
)

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

const maxScanYears = 12

func scanYears(q SearchQuery, currentYear int) []int {
	if len(q.Years) > 0 {
		return boundYears(q.Years, currentYear)
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
	out := make([]int, 0, hi-lo+1)
	for y := lo; y <= hi; y++ {
		out = append(out, y)
	}
	return out
}

func boundYears(years []int, currentYear int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(years))
	for _, y := range years {
		if y < 1970 || y > currentYear+1 || seen[y] {
			continue
		}
		seen[y] = true
		out = append(out, y)
		if len(out) == maxScanYears {
			break
		}
	}
	if len(out) == 0 {
		return []int{currentYear}
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
