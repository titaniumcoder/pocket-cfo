package api

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

const (
	idRent       = "00000000-0000-4000-8000-000000000001"
	idGym        = "00000000-0000-4000-8000-000000000002"
	idGroceries  = "00000000-0000-4000-8000-000000000003"
	idLaptop     = "00000000-0000-4000-8000-000000000004"
	idAccounting = "00000000-0000-4000-8000-000000000005"
)

const budgetJSON = `{
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "` + idRent + `", "name": "Rent", "amount": 900 },
      { "id": "` + idGym + `", "name": "Gym", "amount": 40, "overrides": [{ "month": "2026-08-01", "amount": 0 }] }
    ]},
    { "name": "Food", "kind": "private", "categories": [
      { "id": "` + idGroceries + `", "name": "Groceries", "amount": 350, "minimal_amount": 100 }
    ]},
    { "name": "Equipment", "kind": "company", "categories": [
      { "id": "` + idLaptop + `", "name": "Laptop", "amount": 1800, "date": "2026-10-01" },
      { "id": "` + idAccounting + `", "name": "Accounting", "amount": 150 }
    ]}
  ]
}`

const accountsJSON = `{"accounts":[
  {"name":"Private Checking","kind":"private","balances":[{"as_of":"2026-07-31","balance":4200}]},
  {"name":"Company Checking","kind":"company","balances":[{"as_of":"2026-07-31","balance":8000}]}
]}`

// August: partial coverage, a laptop budgeted for October but bought now, an
// ignored salary, and a grocery overspend.
const august = `{
  "month": "2026-08",
  "coverage": [{ "account": "Private Checking", "from": "2026-08-01", "to": "2026-08-09", "imported_at": "2026-08-10" }],
  "transactions": [
    { "id": "a1", "date": "2026-08-01", "description": "MIETE AUGUST", "amount": 900, "account": "Private Checking", "category": "` + idRent + `" },
    { "id": "a2", "date": "2026-08-03", "description": "LIDL SOFIA 4412", "amount": 210.4, "account": "Private Checking", "category": "` + idGroceries + `" },
    { "id": "a3", "date": "2026-08-05", "description": "NOTEBOOK STORE", "amount": 1800, "account": "Company Checking", "category": "` + idLaptop + `" },
    { "id": "a4", "date": "2026-08-02", "description": "SEPA CREDIT SALARY", "amount": -2400, "account": "Private Checking", "ignored": "salary" }
  ]
}`

const september = `{
  "month": "2026-09",
  "coverage": [{ "account": "Private Checking", "from": "2026-09-01", "to": "2026-09-30", "imported_at": "2026-10-01" }],
  "transactions": [
    { "id": "s1", "date": "2026-09-02", "description": "LIDL SOFIA 4412", "amount": 190, "account": "Private Checking", "category": "` + idGroceries + `" }
  ]
}`

// October is the month the laptop is budgeted for, reconciled and with the
// laptop nowhere in it — it was already paid in August. Nothing in this file
// says so, which is the whole point: the second direction of the mistimed
// check is invisible from one month's transactions.
const october = `{
  "month": "2026-10",
  "coverage": [{ "account": "Private Checking", "from": "2026-10-01", "to": "2026-10-31", "imported_at": "2026-11-01" }],
  "transactions": [
    { "id": "o1", "date": "2026-10-02", "description": "LIDL SOFIA 4412", "amount": 205, "account": "Private Checking", "category": "` + idGroceries + `" }
  ]
}`

func newService(t *testing.T) *Service {
	t.Helper()
	budgetFS := fstest.MapFS{
		"budget.json":   &fstest.MapFile{Data: []byte(budgetJSON)},
		"accounts.json": &fstest.MapFile{Data: []byte(accountsJSON)},
	}
	actualsFS := fstest.MapFS{
		"actuals/2026-08.json": &fstest.MapFile{Data: []byte(august)},
		"actuals/2026-09.json": &fstest.MapFile{Data: []byte(september)},
		"actuals/2026-10.json": &fstest.MapFile{Data: []byte(october)},
	}
	return &Service{
		Budget:   &tracker.Budget{FS: budgetFS},
		Accounts: &tracker.Accounts{FS: budgetFS},
		Actuals:  &tracker.Actuals{FS: actualsFS},
	}
}

func TestParseMonth(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"2026-08", false},
		{"2026-01", false},
		{"2026-12", false},
		{"2026-13", true},
		{"2026-00", true},
		{"2026-8", true},
		{"202608", true},
		{"", true},
		{"2026-08-01", true},
		{"abcd-ef", true},
	}
	for _, tt := range tests {
		_, _, err := ParseMonth(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseMonth(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if err != nil {
			if e, ok := err.(*Error); !ok || e.Code != CodeInvalidRequest {
				t.Errorf("ParseMonth(%q) = %v, want an *Error with code %s", tt.in, err, CodeInvalidRequest)
			}
		}
	}
}

func TestCategories(t *testing.T) {
	got, err := newService(t).Categories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d categories, want 5", len(got))
	}
	// Sorted by id, so the surface is stable between calls.
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Errorf("not sorted by id: %s before %s", got[i-1].ID, got[i].ID)
		}
	}
	byID := map[string]Category{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if c := byID[idLaptop]; c.Kind != "company" || c.Date != "2026-10-01" || c.Group != "Equipment" {
		t.Errorf("Laptop = %+v", c)
	}
	if c := byID[idRent]; c.Kind != "private" || c.Date != "" {
		t.Errorf("Rent = %+v, want private with no date", c)
	}
}

func TestBudgetForMonth(t *testing.T) {
	s := newService(t)
	mb, err := s.BudgetForMonth(context.Background(), "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]PlannedCategory{}
	for _, c := range mb.Categories {
		byID[c.ID] = c
	}
	if got := byID[idRent].PlannedCents; got != 90000 {
		t.Errorf("Rent = %d, want 90000", got)
	}
	if c := byID[idGym]; c.PlannedCents != 0 || !c.Overridden {
		t.Errorf("Gym = %+v, want 0 and overridden", c)
	}
	if got := byID[idLaptop].PlannedCents; got != 0 {
		t.Errorf("Laptop in August = %d, want 0 — it's due in October", got)
	}
	// Groceries has a minimal_amount; the API must report the base.
	if got := byID[idGroceries].PlannedCents; got != 35000 {
		t.Errorf("Groceries = %d, want the base 35000", got)
	}
	if mb.TotalPrivateCents != 90000+0+35000 {
		t.Errorf("TotalPrivateCents = %d", mb.TotalPrivateCents)
	}
	if mb.TotalCompanyCents != 15000 {
		t.Errorf("TotalCompanyCents = %d, want 15000 (Accounting only)", mb.TotalCompanyCents)
	}

	october, err := s.BudgetForMonth(context.Background(), "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range october.Categories {
		if c.ID == idLaptop && c.PlannedCents != 180000 {
			t.Errorf("Laptop in October = %d, want 180000", c.PlannedCents)
		}
	}
}

func TestBudgetForMonthRejectsABadMonth(t *testing.T) {
	_, err := newService(t).BudgetForMonth(context.Background(), "2026-13")
	if err == nil {
		t.Fatal("want an error")
	}
	if e, ok := err.(*Error); !ok || e.Code != CodeInvalidRequest {
		t.Errorf("err = %v, want %s", err, CodeInvalidRequest)
	}
}

func TestBudgetForYear(t *testing.T) {
	months, err := newService(t).BudgetForYear(context.Background(), "2026")
	if err != nil {
		t.Fatal(err)
	}
	if len(months) != 12 {
		t.Fatalf("got %d months, want 12", len(months))
	}
	if months[0].Month != "2026-01" || months[11].Month != "2026-12" {
		t.Errorf("months run %s..%s, want 2026-01..2026-12", months[0].Month, months[11].Month)
	}
	// The one-off lands in exactly one bucket.
	found := 0
	for _, m := range months {
		for _, c := range m.Categories {
			if c.ID == idLaptop && c.PlannedCents > 0 {
				found++
				if m.Month != "2026-10" {
					t.Errorf("Laptop planned in %s, want 2026-10", m.Month)
				}
			}
		}
	}
	if found != 1 {
		t.Errorf("Laptop appears with a figure in %d months, want 1", found)
	}
}

func TestBudgetForYearRejectsABadYear(t *testing.T) {
	for _, y := range []string{"abc", "", "26", "99999"} {
		if _, err := newService(t).BudgetForYear(context.Background(), y); err == nil {
			t.Errorf("BudgetForYear(%q) = nil error, want one", y)
		}
	}
}

func TestAccountsList(t *testing.T) {
	got, err := newService(t).AccountsList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Company Checking" || got[1].Name != "Private Checking" {
		t.Errorf("accounts = %+v, want both, sorted", got)
	}
	// Which pot a card belongs to is the thing an agent cannot infer from the
	// name, and it decides which side of the payroll cascade a line lands on.
	if got[0].Kind != "company" || got[1].Kind != "private" {
		t.Errorf("kinds = %q/%q, want company/private", got[0].Kind, got[1].Kind)
	}
}

func TestActualsFor(t *testing.T) {
	s := newService(t)
	got, err := s.ActualsFor(context.Background(), "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if got.Month != "2026-08" || len(got.Transactions) != 4 {
		t.Errorf("got %+v", got)
	}

	_, err = s.ActualsFor(context.Background(), "2026-07")
	if err == nil {
		t.Fatal("want not_found for an unreconciled month")
	}
	if e, ok := err.(*Error); !ok || e.Code != CodeNotFound {
		t.Errorf("err = %v, want %s", err, CodeNotFound)
	}
}

func TestSearch(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	years := []int{2026}

	t.Run("matches a description across months, newest first", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Query: "LIDL", Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 3 {
			t.Fatalf("got %d matches, want 3", len(got.Transactions))
		}
		if got.Transactions[0].Month != "2026-10" {
			t.Errorf("first match is from %s, want the newest month first", got.Transactions[0].Month)
		}
		if got.Transactions[0].Category != idGroceries {
			t.Errorf("category = %q, want the id it was assigned to", got.Transactions[0].Category)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Query: "lidl sofia", Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 3 {
			t.Errorf("got %d matches, want 3", len(got.Transactions))
		}
	})

	t.Run("no match returns an empty list, not null", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Query: "NOTHING MATCHES", Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if got.Transactions == nil {
			t.Error("Transactions is nil; it must serialise as [] rather than null")
		}
		if len(got.Transactions) != 0 {
			t.Errorf("got %d matches, want 0", len(got.Transactions))
		}
	})

	t.Run("empty query returns everything", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 5 {
			t.Errorf("got %d, want 5 non-ignored transactions", len(got.Transactions))
		}
	})

	t.Run("ignored lines are excluded unless asked for", func(t *testing.T) {
		without, err := s.Search(ctx, SearchQuery{Query: "SALARY", Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(without.Transactions) != 0 {
			t.Errorf("got %d, want the ignored salary excluded", len(without.Transactions))
		}
		with, err := s.Search(ctx, SearchQuery{Query: "SALARY", IncludeIgnored: true, Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(with.Transactions) != 1 || with.Transactions[0].Ignored != "salary" {
			t.Errorf("got %+v, want the salary with its reason", with.Transactions)
		}
	})

	t.Run("limit truncates and says so", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Limit: 1, Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 1 || !got.Truncated {
			t.Errorf("got %d transactions truncated=%v, want 1 and true", len(got.Transactions), got.Truncated)
		}
	})

	t.Run("limit is capped", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Limit: 10000, Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if got.Truncated {
			t.Error("a 5-transaction fixture should not truncate at the cap")
		}
	})

	t.Run("from and to narrow the range", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Query: "LIDL", From: "2026-09", Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 2 || got.Transactions[0].Month != "2026-10" {
			t.Errorf("got %+v, want September onwards, newest first", got.Transactions)
		}
		got, err = s.Search(ctx, SearchQuery{Query: "LIDL", To: "2026-08", Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 1 || got.Transactions[0].Month != "2026-08" {
			t.Errorf("got %+v, want only August", got.Transactions)
		}
	})

	t.Run("category and account filters", func(t *testing.T) {
		got, err := s.Search(ctx, SearchQuery{Category: idRent, Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 1 || got.Transactions[0].ID != "a1" {
			t.Errorf("got %+v, want the rent line", got.Transactions)
		}
		got, err = s.Search(ctx, SearchQuery{Account: "Company Checking", Years: years})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Transactions) != 1 || got.Transactions[0].ID != "a3" {
			t.Errorf("got %+v, want the company line", got.Transactions)
		}
	})

	t.Run("a malformed range is rejected", func(t *testing.T) {
		if _, err := s.Search(ctx, SearchQuery{From: "2026-13", Years: years}); err == nil {
			t.Error("want an error for a bad from")
		}
		if _, err := s.Search(ctx, SearchQuery{To: "nope", Years: years}); err == nil {
			t.Error("want an error for a bad to")
		}
	})
}

func TestServicePropagatesABrokenBudget(t *testing.T) {
	s := &Service{
		Budget:   &tracker.Budget{FS: fstest.MapFS{}},
		Accounts: &tracker.Accounts{FS: fstest.MapFS{}},
		Actuals:  &tracker.Actuals{FS: fstest.MapFS{}},
	}
	ctx := context.Background()
	if _, err := s.Categories(ctx); err == nil {
		t.Error("Categories: want an error when budget.json is missing")
	}
	if _, err := s.BudgetForMonth(ctx, "2026-08"); err == nil {
		t.Error("BudgetForMonth: want an error")
	}
	// accounts.json is an optional layer, so its absence is not an error.
	if _, err := s.AccountsList(ctx); err != nil {
		t.Errorf("AccountsList: %v, want no error for a missing optional file", err)
	}
}

// TestSearchDefaultsToTheCurrentYear pins that a caller passing no Years still
// gets a sensible scan rather than nothing. Pinned to a fixed now rather than
// the wall clock, which would quietly assert nothing at all from 2027.
func TestSearchDefaultsToTheCurrentYear(t *testing.T) {
	s := newService(t)
	s.Now = func() time.Time { return time.Date(2026, time.November, 3, 0, 0, 0, 0, time.UTC) }
	got, err := s.Search(context.Background(), SearchQuery{Query: "LIDL"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Transactions) != 3 {
		t.Errorf("got %d matches for the current year, want 3", len(got.Transactions))
	}
}

// TestSearchDerivesYearsFromTheRange is the bug the REST adapter used to hide:
// a from/to naming a past year must scan that year, whoever is asking.
func TestSearchDerivesYearsFromTheRange(t *testing.T) {
	s := newService(t)
	s.Now = func() time.Time { return time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC) }
	got, err := s.Search(context.Background(), SearchQuery{Query: "LIDL", From: "2026-09", To: "2026-09"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Transactions) != 1 || got.Transactions[0].Month != "2026-09" {
		t.Fatalf("got %+v, want September 2026 — the range says which year", got.Transactions)
	}
}

func TestScanYears(t *testing.T) {
	tests := []struct {
		name string
		q    SearchQuery
		want []int
	}{
		{"nothing given falls back to now", SearchQuery{}, []int{2026}},
		{"explicit wins", SearchQuery{Years: []int{2024}, From: "2019-01"}, []int{2024}},
		{"from alone", SearchQuery{From: "2025-03"}, []int{2025}},
		{"to alone", SearchQuery{To: "2025-03"}, []int{2025}},
		{"the span is filled in", SearchQuery{From: "2024-01", To: "2026-12"}, []int{2024, 2025, 2026}},
		{"a nonsense bound is not a year", SearchQuery{From: "no"}, []int{2026}},
		{"an out-of-range bound is not a year", SearchQuery{From: "0001-01"}, []int{2026}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanYears(tt.q, 2026)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestBudgetForMonthMatchesTheDashboard is the plan's central API assertion:
// the figures an agent matches against must be the ones the page shows. Drift
// here would poison its category matching silently.
func TestBudgetForMonthMatchesTheDashboard(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)

	for _, month := range []time.Month{time.January, time.August, time.October, time.December} {
		key := monthKey(2026, month)
		t.Run(key, func(t *testing.T) {
			view, err := s.Budget.ForMonth(ctx, 2026, month, now, false)
			if err != nil {
				t.Fatal(err)
			}
			mb, err := s.BudgetForMonth(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if mb.TotalPrivateCents != view.TotalPlannedCents {
				t.Errorf("private total: API %d, dashboard %d", mb.TotalPrivateCents, view.TotalPlannedCents)
			}
			if mb.TotalCompanyCents != view.CompanyTotalPlannedCents {
				t.Errorf("company total: API %d, dashboard %d", mb.TotalCompanyCents, view.CompanyTotalPlannedCents)
			}

			rows := map[string]int{}
			for _, g := range append(append([]tracker.CategoryGroupView{}, view.Groups...), view.CompanyGroups...) {
				for _, r := range g.Rows {
					rows[r.CategoryID] = r.PlannedCents
				}
			}
			for _, c := range mb.Categories {
				if want, ok := rows[c.ID]; ok && want != c.PlannedCents {
					t.Errorf("%s: API %d, dashboard row %d", c.Name, c.PlannedCents, want)
				}
			}
		})
	}
}

const splitActuals = `{
  "month": "2026-08",
  "coverage": [{ "account": "Private Checking", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }],
  "transactions": [
    { "id": "w1", "date": "2026-08-14", "description": "ATM WITHDRAWAL SOFIA", "amount": 100, "account": "Private Checking",
      "splits": [
        { "amount": 30, "ignored": "cash still in my pocket" },
        { "amount": 70, "category": "` + idGroceries + `" }
      ] }
  ]
}`

// The ignored part is deliberately first in splitActuals: code that reaches
// for one part instead of walking them all then gets the wrong answer here,
// rather than the right one by accident.
func splitService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		Budget:   &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(budgetJSON)}}},
		Accounts: &tracker.Accounts{FS: fstest.MapFS{}},
		Actuals:  &tracker.Actuals{FS: fstest.MapFS{"actuals/2026-08.json": &fstest.MapFile{Data: []byte(splitActuals)}}},
	}
}

// TestSearchReportsSplitParts: an agent searches history to learn how a merchant
// was treated last time, so "70 groceries, 30 pocket, out of 100" is the
// answer — one result carrying its parts, not a result per part.
func TestSearchReportsSplitParts(t *testing.T) {
	s := splitService(t)
	ctx := context.Background()

	got, err := s.Search(ctx, SearchQuery{Query: "ATM", Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Transactions) != 1 {
		t.Fatalf("got %d results, want the one statement line", len(got.Transactions))
	}
	tx := got.Transactions[0]
	if tx.Category != "" || tx.Ignored != "" {
		t.Errorf("a split line reported a single category: %+v", tx)
	}
	if len(tx.Splits) != 2 || tx.Splits[0].Ignored == "" || tx.Splits[1].Amount != 70 {
		t.Errorf("splits = %+v, want the two parts", tx.Splits)
	}

	// Filtering by a category one part carries must find the line.
	byCat, err := s.Search(ctx, SearchQuery{Category: idGroceries, Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byCat.Transactions) != 1 {
		t.Errorf("filtering by a split's category found %d lines, want 1", len(byCat.Transactions))
	}

	// And a line whose parts are not all ignored is not an ignored line.
	noIgnored, err := s.Search(ctx, SearchQuery{Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	if len(noIgnored.Transactions) != 1 {
		t.Errorf("the default search dropped a partly-ignored line: %d results", len(noIgnored.Transactions))
	}
}

const untrackedActuals = `{
  "month": "2026-08",
  "coverage": [{ "account": "Private Checking", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }],
  "transactions": [
    { "id": "u1", "date": "2026-08-03", "description": "LIDL SOFIA 4412", "amount": 40, "account": "Private Checking", "category": "` + idGroceries + `" },
    { "id": "u2", "date": "2026-08-14", "description": "ATM WITHDRAWAL SOFIA", "amount": 100, "account": "Private Checking", "untracked": "cash, not spent yet" },
    { "id": "u3", "date": "2026-08-20", "description": "ATM WITHDRAWAL VARNA", "amount": 50, "account": "Private Checking",
      "splits": [
        { "amount": 20, "category": "` + idGroceries + `" },
        { "amount": 30, "untracked": "still in my wallet" }
      ] }
  ]
}`

func untrackedService(t *testing.T) *Service {
	t.Helper()
	return &Service{
		Budget:   &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(budgetJSON)}}},
		Accounts: &tracker.Accounts{FS: fstest.MapFS{}},
		Actuals:  &tracker.Actuals{FS: fstest.MapFS{"actuals/2026-08.json": &fstest.MapFile{Data: []byte(untrackedActuals)}}},
	}
}

// TestSearchFindsUntrackedLines: parking cash under untracked is only useful
// if it can be found again, so it is in the results by default and
// OnlyUntracked narrows to exactly the outstanding decisions.
func TestSearchFindsUntrackedLines(t *testing.T) {
	s := untrackedService(t)
	ctx := context.Background()

	all, err := s.Search(ctx, SearchQuery{Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Transactions) != 3 {
		t.Fatalf("default search returned %d lines, want all 3 — untracked must not be hidden like ignored", len(all.Transactions))
	}

	only, err := s.Search(ctx, SearchQuery{OnlyUntracked: true, Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	if len(only.Transactions) != 2 {
		t.Fatalf("OnlyUntracked returned %d lines, want the 2 with something undecided", len(only.Transactions))
	}
	if only.Transactions[0].Untracked != "cash, not spent yet" {
		t.Errorf("whole-line untracked note lost: %+v", only.Transactions[0])
	}
	if got := only.Transactions[1].Splits; len(got) != 2 || got[1].Untracked != "still in my wallet" {
		t.Errorf("split untracked note lost: %+v", got)
	}
}

func TestScanYearsIsBounded(t *testing.T) {
	const current = 2026

	t.Run("an absurd range is capped", func(t *testing.T) {
		var many []int
		for y := 1; y <= 9999; y++ {
			many = append(many, y)
		}
		got := scanYears(SearchQuery{Years: many}, current)
		if len(got) > maxScanYears {
			t.Errorf("%d years scanned, want at most %d", len(got), maxScanYears)
		}
		for _, y := range got {
			if y < 1970 || y > current+1 {
				t.Errorf("year %d is outside anything plausible", y)
			}
		}
	})

	t.Run("an ordinary request is untouched", func(t *testing.T) {
		got := scanYears(SearchQuery{Years: []int{2025, 2026}}, current)
		if len(got) != 2 || got[0] != 2025 || got[1] != 2026 {
			t.Errorf("years = %v, want [2025 2026]", got)
		}
	})

	t.Run("nothing plausible falls back to this year", func(t *testing.T) {
		got := scanYears(SearchQuery{Years: []int{1066, 9999}}, current)
		if len(got) != 1 || got[0] != current {
			t.Errorf("years = %v, want [%d]", got, current)
		}
	})
}
