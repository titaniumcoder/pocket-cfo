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

func edit(t *testing.T, s *Service, req EditRequest) (*EditResult, error) {
	t.Helper()
	return s.EditTransactions(context.Background(), req)
}

// dispositionOf renders whichever of the four a committed line ended up with,
// so a test can assert on the whole answer rather than one field at a time.
func dispositionOf(tx actualsdata.Transaction) string {
	switch {
	case tx.Category != nil:
		return "category:" + *tx.Category
	case tx.Ignored != nil:
		return "ignored:" + *tx.Ignored
	case tx.Untracked != nil:
		return "untracked:" + *tx.Untracked
	case len(tx.Splits) > 0:
		return "splits"
	}
	return "none"
}

func lineIn(t *testing.T, af actualsdata.ActualsFile, id string) actualsdata.Transaction {
	t.Helper()
	for _, tx := range af.Transactions {
		if tx.Id == id {
			return tx
		}
	}
	t.Fatalf("no transaction %s in %s", id, af.Month)
	return actualsdata.Transaction{}
}

// parkmartAugust is the situation that motivated this endpoint: several lines
// from one merchant, recorded under the wrong category, plus unrelated lines
// that must come through untouched.
const parkmartAugust = `{
  "month": "2026-08",
  "coverage": [{ "account": "A", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }],
  "transactions": [
    {"id":"p1","date":"2026-08-03","description":"PARKMART SOFIA","amount":12.5,"account":"A","category":"` + idRent + `"},
    {"id":"rent","date":"2026-08-01","description":"MIETE","amount":900,"account":"A","category":"` + idRent + `"},
    {"id":"p2","date":"2026-08-11","description":"PARKMART SOFIA","amount":8,"account":"A","category":"` + idRent + `"},
    {"id":"p3","date":"2026-08-24","description":"PARKMART VARNA","amount":21,"account":"A","ignored":"thought it was a transfer"}
  ]
}`

// TestEditRecategorisesABatchInOneCommit is Hermes' actual task: every
// Parkmart line in August to Groceries, without resending the month.
func TestEditRecategorisesABatchInOneCommit(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
	s := writeService(t, gh)

	got, err := edit(t, s, EditRequest{
		Reason: "Parkmart is a supermarket, not the landlord",
		Edits: []Edit{
			{ID: "p1", Month: "2026-08", Category: strp(idGroceries)},
			{ID: "p2", Month: "2026-08", Category: strp(idGroceries)},
			{ID: "p3", Month: "2026-08", Category: strp(idGroceries)},
		},
	})
	if err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}
	if got.Edited != 3 {
		t.Errorf("Edited = %d, want 3", got.Edited)
	}
	if gh.puts != 1 {
		t.Fatalf("PUT called %d times, want 1 — a batch is one commit", gh.puts)
	}
	if !strings.Contains(gh.lastMsg, "Parkmart is a supermarket") {
		t.Errorf("commit message lost the reason:\n%s", gh.lastMsg)
	}

	aug := committed(t, gh, augustPath)
	if len(aug.Transactions) != 4 {
		t.Fatalf("got %d transactions, want the same 4", len(aug.Transactions))
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if got := dispositionOf(lineIn(t, aug, id)); got != "category:"+idGroceries {
			t.Errorf("%s = %s, want the new category", id, got)
		}
	}
	// p3 was ignored; setting a category must have cleared that, or the line
	// would carry two answers.
	if p3 := lineIn(t, aug, "p3"); p3.Ignored != nil {
		t.Errorf("p3 kept its ignored reason alongside a category: %+v", p3)
	}
	// And the line nobody mentioned is byte-for-byte what it was.
	rent := lineIn(t, aug, "rent")
	if dispositionOf(rent) != "category:"+idRent || rent.Amount != 900 || rent.Description != "MIETE" {
		t.Errorf("an unmentioned line changed: %+v", rent)
	}
}

// TestEditCannotTouchWhatTheBankSaid: the statement facts are not in the Edit
// type at all, so a line's date, amount, description and account survive
// whatever is done to its attribution.
func TestEditCannotTouchWhatTheBankSaid(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
	s := writeService(t, gh)

	if _, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "p1", Month: "2026-08", Untracked: strp("no idea what this was")},
	}}); err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}

	p1 := lineIn(t, committed(t, gh, augustPath), "p1")
	if p1.Date != "2026-08-03" || p1.Amount != 12.5 || p1.Description != "PARKMART SOFIA" || p1.Account != "A" {
		t.Errorf("statement facts changed: %+v", p1)
	}
	if dispositionOf(p1) != "untracked:no idea what this was" {
		t.Errorf("disposition = %s", dispositionOf(p1))
	}
}

// TestEditReplacesADispositionWholesale walks a line through every state, each
// one clearing the last. Ending up with two answers and no rule for which
// wins is the failure this guards.
func TestEditReplacesADispositionWholesale(t *testing.T) {
	for _, tt := range []struct {
		name string
		e    Edit
		want string
	}{
		{"to a category", Edit{Category: strp(idGroceries)}, "category:" + idGroceries},
		{"to ignored", Edit{Ignored: strp("transfer to savings")}, "ignored:transfer to savings"},
		{"to untracked", Edit{Untracked: strp("cash, not spent yet")}, "untracked:cash, not spent yet"},
		{"to splits", Edit{Splits: []actualsdata.Split{
			{Amount: 8, Category: strp(idGroceries)},
			{Amount: 4.5, Untracked: strp("the rest is in my wallet")},
		}}, "splits"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
			s := writeService(t, gh)

			tt.e.ID, tt.e.Month = "p1", "2026-08"
			if _, err := edit(t, s, EditRequest{Edits: []Edit{tt.e}}); err != nil {
				t.Fatalf("EditTransactions: %v", err)
			}
			p1 := lineIn(t, committed(t, gh, augustPath), "p1")
			if got := dispositionOf(p1); got != tt.want {
				t.Errorf("disposition = %s, want %s", got, tt.want)
			}
			if countSet(p1.Category != nil, p1.Ignored != nil, p1.Untracked != nil, len(p1.Splits) > 0) != 1 {
				t.Errorf("line carries more than one answer: %+v", p1)
			}
		})
	}
}

// TestEditRefusesAnUnknownIdAndCommitsNothing: an id that isn't there usually
// means the wrong month or a stale list, and half-applying the batch would
// report success for work that was not done.
func TestEditRefusesAnUnknownIdAndCommitsNothing(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
	s := writeService(t, gh)

	_, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "p1", Month: "2026-08", Category: strp(idGroceries)},
		{ID: "nope", Month: "2026-08", Category: strp(idGroceries)},
	}})
	if err == nil {
		t.Fatal("accepted an unknown id")
	}
	if e, ok := err.(*Error); !ok || e.Code != CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
	if !strings.Contains(err.Error(), "nothing in that month was changed") {
		t.Errorf("err = %v, want it to say nothing was written", err)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0", gh.puts)
	}
}

// TestEditSpansMonthsInOneCall: one commit per month touched, oldest first.
func TestEditSpansMonthsInOneCall(t *testing.T) {
	gh := newFakeGitHub(map[string]string{
		augustPath: parkmartAugust,
		"data/actuals/2026-09.json": `{
  "month": "2026-09",
  "coverage": [{ "account": "A", "from": "2026-09-01", "to": "2026-09-30", "imported_at": "2026-10-01" }],
  "transactions": [{"id":"s1","date":"2026-09-02","description":"PARKMART SOFIA","amount":9,"account":"A","category":"` + idRent + `"}]
}`,
	})
	s := writeService(t, gh)

	got, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "s1", Month: "2026-09", Category: strp(idGroceries)},
		{ID: "p1", Month: "2026-08", Category: strp(idGroceries)},
	}})
	if err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}
	if got.Edited != 2 || len(got.Months) != 2 {
		t.Fatalf("result = %+v, want 2 edits across 2 months", got)
	}
	if got.Months[0].Month != "2026-08" || got.Months[1].Month != "2026-09" {
		t.Errorf("months = %+v, want oldest first", got.Months)
	}
}

// TestEditFindsTheMonthWhenNotToldOne: convenient, but the lookup reads the
// deployed checkout, so passing month stays the reliable answer.
func TestEditFindsTheMonthWhenNotToldOne(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
	srv := gh.server(t)
	s := &Service{
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(writeBudgetJSON)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{"actuals/2026-08.json": &fstest.MapFile{Data: []byte(parkmartAugust)}}},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
		Now:     func() time.Time { return time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) },
	}

	got, err := edit(t, s, EditRequest{Edits: []Edit{{ID: "p1", Category: strp(idGroceries)}}})
	if err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}
	if got.Edited != 1 || len(got.Months) != 1 || got.Months[0].Month != "2026-08" {
		t.Errorf("result = %+v, want the August line found and edited", got)
	}

	// An id nowhere on disk says what to do about it rather than guessing.
	_, err = edit(t, s, EditRequest{Edits: []Edit{{ID: "ghost", Category: strp(idGroceries)}}})
	if err == nil || !strings.Contains(err.Error(), "pass month") {
		t.Fatalf("err = %v, want advice to pass month", err)
	}
}

// TestEditThatChangesNothingCommitsNothing: re-sending an edit that has
// already been applied must not redeploy the app.
func TestEditThatChangesNothingCommitsNothing(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
	s := writeService(t, gh)

	got, err := edit(t, s, EditRequest{Edits: []Edit{{ID: "rent", Month: "2026-08", Category: strp(idRent)}}})
	if err != nil {
		t.Fatalf("EditTransactions: %v", err)
	}
	if got.Edited != 0 || len(got.Unchanged) != 1 || got.Unchanged[0] != "rent" {
		t.Errorf("result = %+v, want the edit reported as a no-op", got)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0", gh.puts)
	}
}

// TestEditRejectsAMalformedBatchBeforeReadingAnything: everything judgeable
// without a file is judged first, so a bad batch costs no round trips.
func TestEditRejectsAMalformedBatchBeforeReadingAnything(t *testing.T) {
	for _, tt := range []struct {
		name string
		e    Edit
		want string
	}{
		{"nothing set", Edit{ID: "p1"}, "says nothing to change"},
		{"two set", Edit{ID: "p1", Category: strp(idGroceries), Ignored: strp("x")}, "more than one"},
		{"empty category", Edit{ID: "p1", Category: strp("")}, "no way to leave a line undecided"},
		{"blank ignored", Edit{ID: "p1", Ignored: strp("  ")}, "needs a reason"},
		{"blank untracked", Edit{ID: "p1", Untracked: strp("")}, "needs a note"},
		{"one split", Edit{ID: "p1", Splits: []actualsdata.Split{{Amount: 12.5, Category: strp(idGroceries)}}}, "one split is just a category"},
		{"no id", Edit{Category: strp(idGroceries)}, "has no id"},
		{"bad month", Edit{ID: "p1", Month: "August", Category: strp(idGroceries)}, "must look like"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
			s := writeService(t, gh)

			_, err := edit(t, s, EditRequest{Edits: []Edit{tt.e}})
			if err == nil {
				t.Fatalf("accepted %+v", tt.e)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one about %q", err, tt.want)
			}
			if gh.puts != 0 {
				t.Errorf("PUT called %d times, want 0", gh.puts)
			}
		})
	}
}

// TestEditRejectsTheSameIdTwice: two edits for one line means the caller has
// two intentions and no way to say which is final.
func TestEditRejectsTheSameIdTwice(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
	s := writeService(t, gh)

	_, err := edit(t, s, EditRequest{Edits: []Edit{
		{ID: "p1", Month: "2026-08", Category: strp(idGroceries)},
		{ID: "p1", Month: "2026-08", Ignored: strp("no, skip it")},
	}})
	if err == nil || !strings.Contains(err.Error(), "already changed") {
		t.Fatalf("err = %v, want a refusal of the duplicate", err)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0", gh.puts)
	}
}

// TestEditStillValidates: an edit is not a way around the rules the file
// lives by — an unknown category and a split that doesn't reconcile are both
// refused, and nothing is committed.
func TestEditStillValidates(t *testing.T) {
	for _, tt := range []struct {
		name string
		e    Edit
		want string
	}{
		{"unknown category", Edit{Category: strp("not-a-category")}, "not in budget.json"},
		{"splits that don't add up", Edit{Splits: []actualsdata.Split{
			{Amount: 5, Category: strp(idGroceries)},
			{Amount: 5, Untracked: strp("rest")},
		}}, "add up"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gh := newFakeGitHub(map[string]string{augustPath: parkmartAugust})
			s := writeService(t, gh)

			tt.e.ID, tt.e.Month = "p1", "2026-08"
			_, err := edit(t, s, EditRequest{Edits: []Edit{tt.e}})
			if err == nil {
				t.Fatal("accepted an invalid edit")
			}
			if e, ok := err.(*Error); !ok || e.Code != CodeValidationFailed {
				t.Fatalf("err = %v, want validation_failed", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one about %q", err, tt.want)
			}
			if gh.puts != 0 {
				t.Errorf("PUT called %d times, want 0", gh.puts)
			}
		})
	}
}
