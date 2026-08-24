package api

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// windowBudget marks one recurring category bounded to a month window, so the
// agent-facing surface can be pinned against the one-off semantics: a windowed
// category must never be treated as (or allowed to masquerade as) a one-off.
const windowBudget = `{
  "groups": [
    { "name": "Subscriptions", "kind": "private", "categories": [
      { "id": "` + idRent + `", "name": "Agent Tools", "amount": 180, "until": "2026-08-01" }
    ]}
  ]
}`

func windowService(t *testing.T) *Service {
	t.Helper()
	gh := newFakeGitHub(map[string]string{budgetRepoPath: windowBudget})
	srv := gh.server(t)
	return &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(windowBudget)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}
}

// TestMovePlannedExpenseRefusesABoundedCategory: a recurring cost bounded to a
// from/until window has no one-off month to move, and must say so rather than
// the "recurs monthly" excuse that would mislead an agent about a retired
// category.
func TestMovePlannedExpenseRefusesABoundedCategory(t *testing.T) {
	s := windowService(t)

	_, err := move(t, s, MoveRequest{
		CategoryID: idRent, FromMonth: "2026-07", ToMonth: "2026-08",
		Reason: "agent thinks it is a one-off", BaseSHA: shaOf([]byte(windowBudget)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "bounded to a from/until window") {
		t.Errorf("message = %q, want it to name the from/until window", e.Message)
	}
}

// TestAWindowedCategoryChargedInItsMonthIsNotMistimed: mistimedIn only flags
// a charge whose category is a one-off (has a due month); a bounded recurring
// cost charged inside its active month is ordinary and must pass silently.
func TestAWindowedCategoryChargedInItsMonthIsNotMistimed(t *testing.T) {
	catID := idRent
	cat := tracker.PlannedCategory{
		ID: catID, Group: "Subscriptions", Name: "Agent Tools", Kind: "private",
		Date: "", Until: "2026-08-01",
	}
	af := actualsdata.ActualsFile{Transactions: []actualsdata.Transaction{{
		Account: "bank", Amount: 180, Date: "2026-08-10", Description: "tools", Id: "t1",
		Category: &catID,
	}}}
	idx := map[string]tracker.PlannedCategory{catID: cat}
	charged := map[string][]time.Month{catID: {time.August}}

	out := mistimedIn(af, 2026, time.August, []tracker.PlannedCategory{cat}, idx, charged)
	if len(out) != 0 {
		t.Fatalf("bounded category charged in its active month was reported mistimed: %+v", out)
	}
}
func TestListBudgetCategoriesCarriesTheWindow(t *testing.T) {
	s := windowService(t)
	cat, err := s.Categories(context.Background())
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	if len(cat) != 1 {
		t.Fatalf("got %d categories, want 1", len(cat))
	}
	if cat[0].Until != "2026-08-01" {
		t.Errorf("Until = %q, want 2026-08-01", cat[0].Until)
	}
	if cat[0].From != "" {
		t.Errorf("From = %q, want empty", cat[0].From)
	}
	if cat[0].Date != "" {
		t.Errorf("Date = %q, want empty (a windowed category is not a one-off)", cat[0].Date)
	}
}
