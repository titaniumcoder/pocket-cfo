package api

import (
	"context"

	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func writeAugust(t *testing.T, s *Service) {
	t.Helper()
	_, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{
			addTx("r1", "2026-08-01", 900, idRent),
			{
				Id: "w1", Date: "2026-08-14", Description: "ATM", Amount: 50, Account: "A",
				Untracked: strp("cash, not spent yet"),
			},
		},
		Coverage: []actualsdata.Coverage{cover("2026-08-01", "2026-08-31")},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func monthStatus(t *testing.T, rec []MonthStatus, month string) MonthStatus {
	t.Helper()
	for _, m := range rec {
		if m.Month == month {
			return m
		}
	}
	t.Fatalf("no %s in the reconciliation", month)
	return MonthStatus{}
}

// TestAWriteIsVisibleToEveryReadImmediately is the sequence that misled Hermes:
// it edited a month, and get_reconciliation_status then reported the figures
// from before the edit, because that tool reads the deployed checkout while
// get_actuals reads the store. writeService's Actuals FS is empty, which is
// exactly the "nothing deployed yet" state a fresh commit is in.
func TestAWriteIsVisibleToEveryReadImmediately(t *testing.T) {
	s := writeService(t, newFakeGitHub(nil))
	ctx := context.Background()
	writeAugust(t, s)

	found, err := s.Search(ctx, SearchQuery{Years: []int{2026}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Transactions) != 2 {
		t.Errorf("search found %d of the 2 lines just written", len(found.Transactions))
	}

	rec, err := s.Reconciliation(ctx, 2026)
	if err != nil {
		t.Fatal(err)
	}
	aug := monthStatus(t, rec, "2026-08")
	if !aug.Present {
		t.Fatal("reconciliation says August is unreconciled, moments after writing it")
	}
	if aug.UntrackedCents != 5000 {
		t.Errorf("untracked = %d, want 5000 — the line just parked there", aug.UntrackedCents)
	}
	if aug.ActualCents != 90000 {
		t.Errorf("actual = %d, want 90000 — the categorised line only", aug.ActualCents)
	}
}

// TestAnEditIsVisibleToReconciliation: the write that started this. Moving a
// line to untracked has to move the figure the verify step reads.
func TestAnEditIsVisibleToReconciliation(t *testing.T) {
	s := writeService(t, newFakeGitHub(nil))
	ctx := context.Background()
	writeAugust(t, s)

	if _, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "r1", Month: "2026-08", Untracked: strp("no idea what this was")},
	}}); err != nil {
		t.Fatal(err)
	}

	aug := monthStatus(t, mustReconcile(t, s, ctx), "2026-08")
	if aug.UntrackedCents != 95000 {
		t.Errorf("untracked = %d, want 95000 — both lines are undecided now", aug.UntrackedCents)
	}
	if aug.ActualCents != 0 {
		t.Errorf("actual = %d, want 0 — nothing is attributed any more", aug.ActualCents)
	}
}

// TestTheOverlayStepsAsideWhenTheDeployLands: an entry is dropped once the file
// says the same thing, so nothing lingers in memory claiming an authority it no
// longer has — and no TTL has to be guessed at.
func TestTheOverlayStepsAsideWhenTheDeployLands(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)
	writeAugust(t, s)

	deployed := gh.files[augustPath]
	s.Actuals = &tracker.Actuals{FS: fstest.MapFS{
		"actuals/2026-08.json": &fstest.MapFile{Data: deployed},
	}}
	s.Actuals.Publish("2026-08", deployed)

	aug := monthStatus(t, mustReconcile(t, s, context.Background()), "2026-08")
	if aug.TransactionCount != 2 {
		t.Errorf("after the deploy the month reads %d transactions, want 2", aug.TransactionCount)
	}
}

// TestAPublishedMonthIsStillValidated: the overlay carries bytes through the
// same parse and validate a file gets, so it can never surface a document the
// loader would have refused from disk.
func TestAPublishedMonthIsStillValidated(t *testing.T) {
	bad := []byte(`{"month":"2026-08","coverage":[],"transactions":[]}`)

	fromOverlay := &tracker.Actuals{FS: fstest.MapFS{}}
	fromOverlay.Publish("2026-08", bad)
	_, _, overlayErr := fromOverlay.TransactionsForMonth(context.Background(), 2026, time.August)

	fromDisk := &tracker.Actuals{FS: fstest.MapFS{
		"actuals/2026-08.json": &fstest.MapFile{Data: bad},
	}}
	_, _, diskErr := fromDisk.TransactionsForMonth(context.Background(), 2026, time.August)

	if overlayErr == nil {
		t.Fatal("a document with no coverage was accepted from the overlay")
	}
	if diskErr == nil {
		t.Fatal("the same document was accepted from disk — this test proves nothing")
	}
	if overlayErr.Error() != diskErr.Error() {
		t.Errorf("overlay refused with %q, disk with %q — the overlay must be judged by the same rules", overlayErr, diskErr)
	}
}

// TestAMovedExpenseIsVisibleToTheBudget: move_planned_expense is the one write
// that touches budget.json, so get_budget would otherwise keep reporting the
// one-off in the month it was moved out of.
func TestAMovedExpenseIsVisibleToTheBudget(t *testing.T) {
	gh := newFakeGitHub(map[string]string{"data/budget.json": movableBudgetJSON})
	srv := gh.server(t)
	s := &Service{
		Budget: &tracker.Budget{FS: fstest.MapFS{
			"budget.json": &fstest.MapFile{Data: []byte(movableBudgetJSON)},
		}},
		Store: &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}
	ctx := context.Background()

	before, err := s.BudgetForMonth(ctx, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if before.TotalCompanyCents != 180000 {
		t.Fatalf("October plans %d, want the laptop — this test would prove nothing", before.TotalCompanyCents)
	}

	if _, err := s.MovePlannedExpense(ctx, MoveRequest{
		CategoryID: idLaptop, FromMonth: "2026-10", ToMonth: "2026-11",
		Reason: "bought in November", BaseSHA: shaOf([]byte(movableBudgetJSON)),
	}); err != nil {
		t.Fatal(err)
	}

	after, err := s.BudgetForMonth(ctx, "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	if after.TotalCompanyCents != 180000 {
		t.Errorf("November plans %d right after the move, want 180000", after.TotalCompanyCents)
	}
	gone, err := s.BudgetForMonth(ctx, "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if gone.TotalCompanyCents != 0 {
		t.Errorf("October still plans %d after the move", gone.TotalCompanyCents)
	}
}

const movableBudgetJSON = `{
  "groups": [
    { "name": "Equipment", "kind": "company", "categories": [
      { "id": "` + idLaptop + `", "name": "Laptop", "amount": 1800, "date": "2026-10-01" }
    ]}
  ]
}`

func mustReconcile(t *testing.T, s *Service, ctx context.Context) []MonthStatus {
	t.Helper()
	rec, err := s.Reconciliation(ctx, 2026)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}
