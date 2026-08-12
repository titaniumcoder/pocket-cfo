package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// moveBudget keeps the hand-maintained formatting the rewrite must preserve:
// one category per line, keys in a chosen order, a nested overrides array, and
// one multi-line category.
const moveBudget = `{
  "$schema": "../internal/finance/data/budget.schema.json",
  "groups": [
    {
      "name": "Housing",
      "kind": "private",
      "categories": [
        { "id": "` + idRent + `", "name": "Rent", "amount": 900 },
        { "id": "` + idGym + `", "name": "Gym", "amount": 40, "overrides": [{ "month": "2026-08-01", "amount": 0 }], "note": "Paused" }
      ]
    },
    {
      "name": "Equipment",
      "kind": "company",
      "categories": [
        {
          "id": "` + idLaptop + `",
          "name": "Laptop",
          "amount": 1800,
          "date": "2026-10-01",
          "note": "Replacement dev machine"
        }
      ]
    }
  ]
}
`

const budgetRepoPath = "data/budget.json"

func moveService(t *testing.T, gh *fakeGitHub) *Service {
	t.Helper()
	srv := gh.server(t)
	return &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(moveBudget)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}
}

func move(t *testing.T, s *Service, req MoveRequest) (*MoveResult, error) {
	t.Helper()
	return s.MovePlannedExpense(context.Background(), req)
}

func TestMovePlannedExpense(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: moveBudget})
	s := moveService(t, gh)

	got, err := move(t, s, MoveRequest{
		CategoryID: idLaptop, FromMonth: "2026-10", ToMonth: "2026-08",
		Reason: "bought early, charged in August", BaseSHA: shaOf([]byte(moveBudget)),
	})
	if err != nil {
		t.Fatalf("MovePlannedExpense: %v", err)
	}
	if got.Name != "Laptop" || got.From != "2026-10" || got.To != "2026-08" || !got.DeployPending {
		t.Errorf("result = %+v", got)
	}
	if gh.puts != 1 {
		t.Fatalf("PUT called %d times, want 1", gh.puts)
	}

	out := string(gh.lastBody)
	if !strings.Contains(out, `"date": "2026-08-01"`) {
		t.Errorf("the date was not moved:\n%s", out)
	}
	if strings.Contains(out, `"date": "2026-10-01"`) {
		t.Error("the old date survived")
	}

	// Formatting preserved: only that one line differs.
	diff := 0
	oldLines, newLines := strings.Split(moveBudget, "\n"), strings.Split(out, "\n")
	if len(oldLines) != len(newLines) {
		t.Fatalf("line count changed from %d to %d — the file was reformatted:\n%s", len(oldLines), len(newLines), out)
	}
	for i := range oldLines {
		if oldLines[i] != newLines[i] {
			diff++
		}
	}
	if diff != 1 {
		t.Errorf("%d lines differ, want exactly 1", diff)
	}
	if !strings.Contains(out, `{ "id": "`+idGym+`", "name": "Gym", "amount": 40, "overrides": [{ "month": "2026-08-01", "amount": 0 }], "note": "Paused" }`) {
		t.Error("an unrelated category was reformatted")
	}

	// Structural, not just textual: a duplicate key injected on the same line
	// would still leave "exactly one line differs" true, but Go's unmarshal
	// takes the last value, so this catches it.
	var got2 budgetdata.BudgetFile
	if err := json.Unmarshal(gh.lastBody, &got2); err != nil {
		t.Fatalf("committed budget does not parse: %v", err)
	}
	var orig budgetdata.BudgetFile
	if err := json.Unmarshal([]byte(moveBudget), &orig); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		id     string
		amount float64
	}{{idLaptop, 1800}, {idRent, 900}, {idGym, 40}} {
		c, ok := findCategory(got2, want.id)
		if !ok {
			t.Fatalf("category %s vanished", want.id)
		}
		if c.Amount != want.amount {
			t.Errorf("%s amount = %v, want %v — only the date may change", c.Name, c.Amount, want.amount)
		}
	}
	if len(got2.Groups) != len(orig.Groups) {
		t.Errorf("group count changed from %d to %d", len(orig.Groups), len(got2.Groups))
	}
	if gym, _ := findCategory(got2, idGym); len(gym.Overrides) != 1 {
		t.Errorf("Gym overrides = %v, want them untouched", gym.Overrides)
	}

	if !strings.HasPrefix(gh.lastMsg, "fix(budget): move Laptop from 2026-10 to 2026-08") {
		t.Errorf("commit subject = %q", gh.lastMsg)
	}
	if !strings.Contains(gh.lastMsg, "bought early, charged in August") {
		t.Errorf("the reason is not in the commit message: %q", gh.lastMsg)
	}
}

// TestMovePlannedExpenseRefusals: every way it can be refused, asserting
// nothing was committed rather than only that the response said no.
func TestMovePlannedExpenseRefusals(t *testing.T) {
	good := MoveRequest{
		CategoryID: idLaptop, FromMonth: "2026-10", ToMonth: "2026-08",
		Reason: "bought early", BaseSHA: shaOf([]byte(moveBudget)),
	}
	with := func(f func(*MoveRequest)) MoveRequest {
		r := good
		f(&r)
		return r
	}

	tests := []struct {
		name     string
		req      MoveRequest
		wantCode string
	}{
		{"no category id", with(func(r *MoveRequest) { r.CategoryID = "" }), CodeInvalidRequest},
		{"no reason", with(func(r *MoveRequest) { r.Reason = "" }), CodeInvalidRequest},
		{"whitespace reason", with(func(r *MoveRequest) { r.Reason = "  " }), CodeInvalidRequest},
		{"bad from_month", with(func(r *MoveRequest) { r.FromMonth = "2026-13" }), CodeInvalidRequest},
		{"bad to_month", with(func(r *MoveRequest) { r.ToMonth = "nope" }), CodeInvalidRequest},
		{"same month", with(func(r *MoveRequest) { r.ToMonth = r.FromMonth }), CodeInvalidRequest},
		{"unknown category", with(func(r *MoveRequest) { r.CategoryID = "00000000-0000-4000-8000-0000000000ff" }), CodeInvalidRequest},
		{"a recurring category has no month to move", with(func(r *MoveRequest) { r.CategoryID = idRent }), CodeInvalidRequest},
		{"from_month disagrees with the file", with(func(r *MoveRequest) { r.FromMonth = "2026-09" }), CodeConflict},
		{"stale base_sha", with(func(r *MoveRequest) { r.BaseSHA = "stale" }), CodeConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newFakeGitHub(map[string]string{budgetRepoPath: moveBudget})
			s := moveService(t, gh)

			_, err := move(t, s, tt.req)
			if err == nil {
				t.Fatal("want an error")
			}
			e, ok := err.(*Error)
			if !ok {
				t.Fatalf("err = %T %v, want *Error", err, err)
			}
			if e.Code != tt.wantCode {
				t.Errorf("code = %q, want %q (%s)", e.Code, tt.wantCode, e.Message)
			}
			if gh.puts != 0 {
				t.Errorf("PUT called %d times; nothing may reach the repo on a refusal", gh.puts)
			}
			if string(gh.files[budgetRepoPath]) != moveBudget {
				t.Error("budget.json was modified despite the refusal")
			}
		})
	}
}

func TestMovePlannedExpenseWritesNotConfigured(t *testing.T) {
	s := &Service{Budget: &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(moveBudget)}}}}
	_, err := move(t, s, MoveRequest{CategoryID: idLaptop, FromMonth: "2026-10", ToMonth: "2026-08", Reason: "x"})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeWriteNotConfigured {
		t.Fatalf("err = %v, want %s", err, CodeWriteNotConfigured)
	}
}

func TestSetCategoryDatePreservesEverythingElse(t *testing.T) {
	out, err := setCategoryDate([]byte(moveBudget), idLaptop, "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	// The multi-line category keeps its indentation.
	if !strings.Contains(string(out), "\n          \"date\": \"2026-08-01\",\n") {
		t.Errorf("indentation was not preserved:\n%s", out)
	}
	var doc any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
}

// TestSetCategoryDateInsertsWhenAbsent covers a category that has no date at
// all — it can't be reached through MovePlannedExpense, which refuses a
// recurring category, but the byte editor should still be correct.
func TestSetCategoryDateInsertsWhenAbsent(t *testing.T) {
	out, err := setCategoryDate([]byte(moveBudget), idRent, "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `{ "date": "2026-08-01", "id": "`+idRent+`"`) {
		t.Errorf("date was not inserted in place:\n%s", out)
	}
	var doc any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
}

func TestVerifyOnlyDateChangedCatchesTampering(t *testing.T) {
	tampered := strings.Replace(moveBudget, `"amount": 1800`, `"amount": 1900`, 1)
	tampered = strings.Replace(tampered, `"date": "2026-10-01"`, `"date": "2026-08-01"`, 1)
	if err := verifyOnlyDateChanged([]byte(moveBudget), []byte(tampered), idLaptop, "2026-08-01"); err == nil {
		t.Error("a changed amount was accepted")
	}

	clean := strings.Replace(moveBudget, `"date": "2026-10-01"`, `"date": "2026-08-01"`, 1)
	if err := verifyOnlyDateChanged([]byte(moveBudget), []byte(clean), idLaptop, "2026-08-01"); err != nil {
		t.Errorf("a date-only change was rejected: %v", err)
	}
}

// TestVerifyOnlyDateChangedRejectsAnotherCategorysDate proves the guard is
// scoped to the category being moved, not to dates in general.
func TestVerifyOnlyDateChangedRejectsAnotherCategorysDate(t *testing.T) {
	out, err := setCategoryDate([]byte(moveBudget), idLaptop, "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	// Also give an unrelated category a date it never had.
	sneaky := strings.Replace(string(out), `{ "id": "`+idRent+`", "name": "Rent"`, `{ "id": "`+idRent+`", "date": "2027-01-01", "name": "Rent"`, 1)
	if err := verifyOnlyDateChanged([]byte(moveBudget), []byte(sneaky), idLaptop, "2026-08-01"); err == nil {
		t.Error("a second category gaining a date was accepted")
	}
}

func TestMovePlannedExpenseMapsAGitHubRace(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: moveBudget})
	gh.conflict = true
	s := moveService(t, gh)

	_, err := move(t, s, MoveRequest{
		CategoryID: idLaptop, FromMonth: "2026-10", ToMonth: "2026-08",
		Reason: "x", BaseSHA: shaOf([]byte(moveBudget)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
}

// TestMoveToAShorterMonthClampsTheDay: a one-off dated the 31st moved into
// February is an ordinary request. Carrying the day across verbatim produced
// 2026-02-31, which passed the shape check, survived the byte surgery, and
// only died in ValidateBudget — surfacing as a 500 blaming the server for a
// request that was fine.
func TestMoveToAShorterMonthClampsTheDay(t *testing.T) {
	const budget = `{
  "groups": [
    { "name": "Equipment", "kind": "company", "categories": [
      { "id": "` + idLaptop + `", "name": "Laptop", "amount": 1800, "date": "2026-01-31" }
    ]}
  ]
}
`
	gh := newFakeGitHub(map[string]string{budgetRepoPath: budget})
	s := &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(budget)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
	}
	srv := gh.server(t)
	s.Store = &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL}

	_, err := move(t, s, MoveRequest{
		CategoryID: idLaptop, FromMonth: "2026-01", ToMonth: "2026-02",
		Reason: "charged in February", BaseSHA: shaOf([]byte(budget)),
	})
	if err != nil {
		t.Fatalf("MovePlannedExpense: %v", err)
	}
	if out := string(gh.lastBody); !strings.Contains(out, `"date": "2026-02-28"`) {
		t.Errorf("the day was not clamped to the end of February:\n%s", out)
	}
}

func TestMovedDate(t *testing.T) {
	tests := []struct{ date, to, want string }{
		{"2026-10-01", "2026-08", "2026-08-01"},
		{"2026-01-31", "2026-02", "2026-02-28"}, // clamped
		{"2024-01-31", "2024-02", "2024-02-29"}, // and a leap year is longer
		{"2026-03-31", "2026-04", "2026-04-30"},
		{"2026-01-15", "2026-02", "2026-02-15"}, // a day that fits is kept
	}
	for _, tt := range tests {
		got, err := movedDate(tt.date, tt.to)
		if err != nil {
			t.Errorf("movedDate(%q, %q): %v", tt.date, tt.to, err)
			continue
		}
		if got != tt.want {
			t.Errorf("movedDate(%q, %q) = %q, want %q", tt.date, tt.to, got, tt.want)
		}
	}
}
