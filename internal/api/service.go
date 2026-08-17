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
}

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
	Dividends         []DividendPlanned `json:"dividends,omitempty"`
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
	mb.Dividends = s.dividendsIn(ctx, year, m)
	return mb, nil
}

// dividendsIn reports the month's distributions with both taxes worked out,
// so the agent reads the arithmetic rather than recomputing it from rates it
// might resolve to the wrong month.
func (s *Service) dividendsIn(ctx context.Context, year int, month time.Month) []DividendPlanned {
	if s.Budget == nil {
		return nil
	}
	bv, err := s.Budget.ForMonth(ctx, year, month, s.now(), false)
	if err != nil {
		return nil
	}
	return s.Personal.DividendsIn(bv.Dividends, year, month)
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
		mb := monthBudgetOf(key, byMonth[key])
		mb.Dividends = s.dividendsIn(ctx, y, m)
		out = append(out, mb)
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

type Account struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	AsOf string `json:"as_of,omitempty"`

	// Note is set only on the director's loan, which is reported here so an
	// agent knows it exists and otherwise leaves it alone: there is no
	// statement behind it and no balance to read off a bank.
	Note string `json:"note,omitempty"`
}

// KindDirectorLoan is not one of accounts.json's two pots. It is reported
// alongside them because the loan now sits with the accounts on the page, so
// an agent meeting it there must be told what it is rather than inferring a
// bank account nobody has imported.
const KindDirectorLoan = "director_loan"

const loanIsNotAnAccount = "Not a bank account: the running balance between the company and its owner. " +
	"There is no statement to reconcile against it, no coverage to report for it, and record_account_balance does not accept it — " +
	"it is restated by hand in accounts.json, usually once a year with the accountant. Read it with get_director_loan; otherwise ignore it."

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
	if af.DirectorLoan != nil {
		newest := ""
		for _, r := range af.DirectorLoan.Balances {
			if r.AsOf > newest {
				newest = r.AsOf
			}
		}
		out = append(out, Account{Name: "Director's loan", Kind: KindDirectorLoan, AsOf: newest, Note: loanIsNotAnAccount})
	}
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
	Coverage     []actualsdata.Coverage    `json:"coverage"`
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
	Movement    string  `json:"movement,omitempty"`

	Splits []FoundSplit `json:"splits,omitempty"`
}

type FoundSplit struct {
	Amount    float64 `json:"amount"`
	Category  string  `json:"category,omitempty"`
	Ignored   string  `json:"ignored,omitempty"`
	Untracked string  `json:"untracked,omitempty"`
	Movement  string  `json:"movement,omitempty"`
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
	OnlyMovements  bool
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
			if q.OnlyMovements && !anyPart(parts, func(p actualsdata.Part) bool { return p.Movement != "" }) {
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
						Movement: string(p.Movement),
					})
				}
			} else {
				found.Category, found.Ignored, found.Untracked = parts[0].Category, parts[0].Ignored, parts[0].Untracked
				found.Movement = string(parts[0].Movement)
			}
			out.Transactions = append(out.Transactions, found)
		}
	}
	return out, nil
}

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

	UntrackedCount int `json:"untracked_count"`
	UntrackedCents int `json:"untracked_cents"`
}

type MistimedCharge struct {
	CategoryID  string `json:"category_id"`
	Name        string `json:"name"`
	PlannedFor  string `json:"planned_for"`
	ChargedIn   string `json:"charged_in"`
	AmountCents int    `json:"amount_cents"`
}

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
			if err != nil || due == here {
				continue
			}
			seen[id] = true
			out = append(out, MistimedCharge{
				CategoryID: id, Name: cat.Name,
				PlannedFor: due, ChargedIn: here,
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
			AmountCents: c.PlannedCents,
		})
	}
	return out
}

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
