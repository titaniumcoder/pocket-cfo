package api

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// windowBudget marks one recurring category bounded to a month window.
const windowBudget = `{
  "groups": [
    { "name": "Subscriptions", "kind": "private", "categories": [
      { "id": "` + idRent + `", "name": "Agent Tools", "amount": 180, "until": "2026-08-01" }
    ]}
  ]
}`

func windowService(t *testing.T) *Service {
	t.Helper()
	srv := (&fakeGitHub{files: map[string][]byte{budgetRepoPath: []byte(windowBudget)}, blobs: map[string][]byte{}}).server(t)
	return &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(windowBudget)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}
}

// TestListBudgetCategoriesCarriesTheWindow: a recurring cost bounded to a
// from/until window reports its window, and does not read as a one-off.
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
