package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/budgetdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// changeBudget mirrors the hand-maintained shape the rewrite must preserve:
// one-line and multi-line categories, a nested overrides array, and a
// stepped rent so add/correct/remove all have something to bite on.
const changeBudget = `{
  "$schema": "../internal/finance/data/budget.schema.json",
  "groups": [
    {
      "name": "Housing",
      "kind": "private",
      "categories": [
        {
          "id": "` + idRent + `",
          "name": "Rent",
          "amount": 900,
          "amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]
        },
        { "id": "` + idGym + `", "name": "Gym", "amount": 40, "overrides": [{ "month": "2026-11-01", "amount": 0 }], "note": "Paused" }
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
          "date": "2027-06-01",
          "note": "Replacement dev machine"
        }
      ]
    }
  ]
}
`

var changeNow = time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)

func changeService(t *testing.T, gh *fakeGitHub) *Service {
	t.Helper()
	srv := gh.server(t)
	return &Service{
		Now:     func() time.Time { return changeNow },
		Budget:  &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(changeBudget)}}},
		Actuals: &tracker.Actuals{FS: fstest.MapFS{}},
		Store:   &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}
}

func schedule(t *testing.T, s *Service, req ScheduleAmountChangeRequest) (*ScheduleAmountChangeResult, error) {
	t.Helper()
	return s.ScheduleAmountChange(context.Background(), req)
}

// TestScheduleAmountChangeAddsToAFlatCategory: the common case — a rent the
// landlord just put up. The key is added to the multi-line category at its
// own indent, and nothing else in the file moves.
func TestScheduleAmountChangeAddsToAFlatCategory(t *testing.T) {
	flat := strings.Replace(changeBudget,
		`"amount": 900,
          "amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount": 900`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: flat})
	s := changeService(t, gh)

	got, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-06", Amount: f64Ptr(950),
		Reason:  "rent rises with the lease",
		BaseSHA: shaOf([]byte(flat)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	if got.Name != "Rent" || !got.DeployPending || gh.puts != 1 {
		t.Fatalf("result = %+v, PUTs = %d", got, gh.puts)
	}
	out := string(gh.lastBody)
	if !strings.Contains(out, `"amount_changes": [ { "from": "2027-06-01", "amount": 950 } ]`) {
		t.Errorf("the new entry is missing or misformatted:\n%s", out)
	}
	if !strings.HasPrefix(gh.lastMsg, "feat(budget): schedule Rent to 950 from 2027-06") {
		t.Errorf("commit subject = %q", gh.lastMsg)
	}
	if !strings.Contains(gh.lastMsg, "rent rises with the lease") {
		t.Errorf("the reason is not in the commit message: %q", gh.lastMsg)
	}
	assertOnlyThatCategoryChanged(t, flat, out, idRent)
}

// TestScheduleAmountChangeCorrectsAScheduledMonth: sending the same from with
// a new amount rewrites that one entry in place — a correction, and the
// commit says so.
func TestScheduleAmountChangeCorrectsAScheduledMonth(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	got, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-01", Amount: f64Ptr(940),
		Reason:  "landlord meant 940, not 930",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	if !got.Removed && got.Amount == nil {
		t.Errorf("result carries no amount: %+v", got)
	}
	out := string(gh.lastBody)
	if !strings.Contains(out, `{ "from": "2027-01-01", "amount": 940 }`) {
		t.Errorf("the entry was not corrected:\n%s", out)
	}
	if strings.Contains(out, `"amount": 930`) {
		t.Error("the old price survived")
	}
	if !strings.HasPrefix(gh.lastMsg, "fix(budget): correct Rent's scheduled change for 2027-01 to 940") {
		t.Errorf("commit subject = %q", gh.lastMsg)
	}
	assertOnlyThatCategoryChanged(t, changeBudget, out, idRent)
}

// TestScheduleAmountChangeAppendsToAnExistingList: a second step lands after
// the first, and the list keeps growing one entry at a time.
func TestScheduleAmountChangeAddsToAnEmptyList(t *testing.T) {
	emptyList := strings.Replace(changeBudget,
		`"amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount_changes": []`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: emptyList})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-06", Amount: f64Ptr(950),
		Reason:  "the first step",
		BaseSHA: shaOf([]byte(emptyList)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	out := string(gh.lastBody)
	if !strings.Contains(out, `"amount_changes": [ { "from": "2027-06-01", "amount": 950 } ]`) {
		t.Errorf("the entry was not added to the empty list:\n%s", out)
	}
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(gh.lastBody, &bf); err != nil {
		t.Fatalf("committed budget does not parse: %v", err)
	}
	rent, _ := findCategory(bf, idRent)
	if len(rent.AmountChanges) != 1 {
		t.Errorf("rent has %d changes, want 1", len(rent.AmountChanges))
	}
}

func TestScheduleAmountChangeAppendsToAnExistingList(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2028-01", Amount: f64Ptr(990), MinimalAmount: f64Ptr(900),
		Reason:  "and again in 2028",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	out := string(gh.lastBody)
	if !strings.Contains(out, `{ "from": "2028-01-01", "amount": 990, "minimal_amount": 900 }`) {
		t.Errorf("the appended entry is missing or misformatted:\n%s", out)
	}
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(gh.lastBody, &bf); err != nil {
		t.Fatalf("committed budget does not parse: %v", err)
	}
	rent, _ := findCategory(bf, idRent)
	if len(rent.AmountChanges) != 2 {
		t.Fatalf("rent has %d changes, want 2: %+v", len(rent.AmountChanges), rent.AmountChanges)
	}
	assertOnlyThatCategoryChanged(t, changeBudget, out, idRent)
}

func TestScheduleAmountChangeRemovesAnEntry(t *testing.T) {
	two := strings.Replace(changeBudget,
		`"amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount_changes": [ { "from": "2027-01-01", "amount": 930 }, { "from": "2028-01-01", "amount": 990 } ]`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: two})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-01", Remove: true,
		Reason:  "the rise was called off",
		BaseSHA: shaOf([]byte(two)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	out := string(gh.lastBody)
	if !strings.Contains(out, `{ "from": "2028-01-01", "amount": 990 }`) {
		t.Errorf("the surviving entry was lost:\n%s", out)
	}
	if strings.Contains(out, `"2027-01-01"`) {
		t.Error("the removed entry survived")
	}
	if !strings.HasPrefix(gh.lastMsg, "fix(budget): remove Rent's scheduled change for 2027-01") {
		t.Errorf("commit subject = %q", gh.lastMsg)
	}
	assertOnlyThatCategoryChanged(t, two, out, idRent)
}

func TestScheduleAmountChangeRemovalOfTheLastEntryDropsTheKey(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-01", Remove: true,
		Reason:  "the rise was called off",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	out := string(gh.lastBody)
	if strings.Contains(out, "amount_changes") {
		t.Errorf("removing the last entry left the key behind:\n%s", out)
	}
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(gh.lastBody, &bf); err != nil {
		t.Fatalf("committed budget does not parse: %v", err)
	}
	rent, _ := findCategory(bf, idRent)
	if len(rent.AmountChanges) != 0 {
		t.Errorf("rent still has %d changes", len(rent.AmountChanges))
	}
	if rent.Amount != 900 {
		t.Errorf("the base amount moved to %v, want 900", rent.Amount)
	}
	assertOnlyThatCategoryChanged(t, changeBudget, out, idRent)
}

func TestScheduleAmountChangeRefusesAClosedMonth(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2026-10", Amount: f64Ptr(910),
		Reason:  "this month, right now",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "cannot plan a schedule change on an already closed budget") {
		t.Errorf("message = %q, want the closed-budget refusal", e.Message)
	}
	if gh.puts != 0 {
		t.Errorf("a refused change still committed (PUTs = %d)", gh.puts)
	}
}

func TestScheduleAmountChangeRefusesAOneOff(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idLaptop, FromMonth: "2028-01", Amount: f64Ptr(2000),
		Reason:  "the machine gets expensive",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "a single price, full stop") {
		t.Errorf("message = %q, want the one-off refusal", e.Message)
	}
	if gh.puts != 0 {
		t.Errorf("a refused change still committed (PUTs = %d)", gh.puts)
	}
}

func TestScheduleAmountChangeRefusesOutsideTheWindow(t *testing.T) {
	windowed := strings.Replace(changeBudget,
		`"amount": 900,
          "amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount": 900,
          "until": "2027-06-01",
          "amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: windowed})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-07", Amount: f64Ptr(950),
		Reason:  "a change after the category has ended",
		BaseSHA: shaOf([]byte(windowed)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "could never take effect") {
		t.Errorf("message = %q, want the dead-change refusal", e.Message)
	}
	if gh.puts != 0 {
		t.Errorf("a refused change still committed (PUTs = %d)", gh.puts)
	}
}

func TestScheduleAmountChangeRefusesANegativeAmount(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-06", Amount: f64Ptr(-50),
		Reason:  "a discount, somehow",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "never negative") {
		t.Errorf("message = %q, want the negative-price refusal", e.Message)
	}
	if gh.puts != 0 {
		t.Errorf("a refused change still committed (PUTs = %d)", gh.puts)
	}
}

// TestScheduleAmountChangeRefusesMinimalAboveAmount: the validator holds this
// in the file, but a caller mistake must come back as a refusal, not as an
// internal failure out of the diff guard.
func TestScheduleAmountChangeRefusesMinimalAboveAmount(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-06", Amount: f64Ptr(100), MinimalAmount: f64Ptr(200),
		Reason:  "the minimum is bigger than the price",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "above the amount") {
		t.Errorf("message = %q, want the minimal-above-amount refusal", e.Message)
	}
	if gh.puts != 0 {
		t.Errorf("a refused change still committed (PUTs = %d)", gh.puts)
	}
}

// TestScheduleAmountChangeRefusesBeforeTheWindow: the dead-change guard has a
// from side as well as an until one.
func TestScheduleAmountChangeRefusesBeforeTheWindow(t *testing.T) {
	late := strings.Replace(changeBudget,
		`"amount": 900,
          "amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount": 900,
          "from": "2026-12-01",
          "amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: late})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2026-11", Amount: f64Ptr(910),
		Reason:  "a change before the category starts",
		BaseSHA: shaOf([]byte(late)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want %s", err, CodeInvalidRequest)
	}
	if !strings.Contains(e.Message, "could never take effect") {
		t.Errorf("message = %q, want the dead-change refusal", e.Message)
	}
	if gh.puts != 0 {
		t.Errorf("a refused change still committed (PUTs = %d)", gh.puts)
	}
}

func TestScheduleAmountChangeRemovalOfAnUnknownMonthIsNotFound(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2029-05", Remove: true,
		Reason:  "undo something that was never scheduled",
		BaseSHA: shaOf([]byte(changeBudget)),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeNotFound {
		t.Fatalf("err = %v, want %s", err, CodeNotFound)
	}
	if gh.puts != 0 {
		t.Errorf("a refused removal still committed (PUTs = %d)", gh.puts)
	}
}

func TestScheduleAmountChangeRefusesAStaleSHA(t *testing.T) {
	gh := newFakeGitHub(map[string]string{budgetRepoPath: changeBudget})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-06", Amount: f64Ptr(950),
		Reason:  "based on a read from yesterday",
		BaseSHA: strings.Repeat("0", 40),
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want %s", err, CodeConflict)
	}
	dets, ok := e.Details.(map[string]string)
	if !ok || dets["current_sha"] != shaOf([]byte(changeBudget)) {
		t.Errorf("the conflict does not carry the current sha: %+v", e.Details)
	}
}

// TestScheduleAmountChangeAppendsIndentedToAMultiLineList: a hand-written
// list that spans several lines keeps its shape — the new entry lands on its
// own line at the list's indent.
func TestScheduleAmountChangeAppendsIndentedToAMultiLineList(t *testing.T) {
	multi := strings.Replace(changeBudget,
		`"amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount_changes": [
            { "from": "2027-01-01", "amount": 930 }
          ]`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: multi})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2028-01", Amount: f64Ptr(990),
		Reason:  "and again in 2028",
		BaseSHA: shaOf([]byte(multi)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	out := string(gh.lastBody)
	if !strings.Contains(out, "\"amount_changes\": [\n            { \"from\": \"2027-01-01\", \"amount\": 930 },\n            { \"from\": \"2028-01-01\", \"amount\": 990 }\n          ]") {
		t.Errorf("the multi-line list lost its shape:\n%s", out)
	}
	var bf budgetdata.BudgetFile
	if err := json.Unmarshal(gh.lastBody, &bf); err != nil {
		t.Fatalf("committed budget does not parse: %v", err)
	}
}

// TestScheduleAmountChangeRemovalOfTheFirstEntryLeavesNoBlankLine: dropping
// an entry from a multi-line list loses the entry's line, not the value and
// a gap.
func TestScheduleAmountChangeRemovalOfTheFirstEntryLeavesNoBlankLine(t *testing.T) {
	multi := strings.Replace(changeBudget,
		`"amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount_changes": [
            { "from": "2027-01-01", "amount": 930 },
            { "from": "2028-01-01", "amount": 990 }
          ]`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: multi})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-01", Remove: true,
		Reason:  "the first rise was called off",
		BaseSHA: shaOf([]byte(multi)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	out := string(gh.lastBody)
	if !strings.Contains(out, "\"amount_changes\": [\n            { \"from\": \"2028-01-01\", \"amount\": 990 }\n          ]") {
		t.Errorf("removing the first entry left the list misshapen:\n%s", out)
	}
}

// TestScheduleAmountChangeDroppingTheKeyAsFirstKeyLeavesNoBlankLine: a key
// that opens its category is gone with its whole line.
func TestScheduleAmountChangeDroppingTheKeyAsFirstKeyLeavesNoBlankLine(t *testing.T) {
	first := strings.Replace(changeBudget,
		`"id": "`+idRent+`",
          "name": "Rent",
          "amount": 900,
          "amount_changes": [ { "from": "2027-01-01", "amount": 930 } ]`,
		`"amount_changes": [ { "from": "2027-01-01", "amount": 930 } ],
          "id": "`+idRent+`",
          "name": "Rent",
          "amount": 900`, 1)
	gh := newFakeGitHub(map[string]string{budgetRepoPath: first})
	s := changeService(t, gh)

	_, err := schedule(t, s, ScheduleAmountChangeRequest{
		CategoryID: idRent, FromMonth: "2027-01", Remove: true,
		Reason:  "the rise was called off",
		BaseSHA: shaOf([]byte(first)),
	})
	if err != nil {
		t.Fatalf("ScheduleAmountChange: %v", err)
	}
	out := string(gh.lastBody)
	want := `{
          "id": "` + idRent + `",
          "name": "Rent",
          "amount": 900
        },`
	if !strings.Contains(out, want) {
		t.Errorf("dropping the key as first key left a blank line:\n%s", out)
	}
}

// assertOnlyThatCategoryChanged is the structural half of the move-planned-
// expense guard: everything outside the one category's amount_changes must be
// byte-for-byte the same after unmarshalling, and the diff must be small.
func assertOnlyThatCategoryChanged(t *testing.T, before, after string, categoryID string) {
	t.Helper()

	var a, b budgetdata.BudgetFile
	if err := json.Unmarshal([]byte(before), &a); err != nil {
		t.Fatalf("before does not parse: %v", err)
	}
	if err := json.Unmarshal([]byte(after), &b); err != nil {
		t.Fatalf("committed budget does not parse: %v", err)
	}
	for _, g := range a.Groups {
		bg, ok := findGroup(b, g.Name)
		if !ok {
			t.Fatalf("group %s vanished", g.Name)
		}
		if len(g.Categories) != len(bg.Categories) {
			t.Fatalf("group %s lost categories: %d -> %d", g.Name, len(g.Categories), len(bg.Categories))
		}
		for i, c := range g.Categories {
			nc := bg.Categories[i]
			if c.Id != nc.Id || c.Name != nc.Name || c.Amount != nc.Amount || derefPtr(c.Date) != derefPtr(nc.Date) {
				t.Errorf("category %d in group %s changed beyond its amount_changes: %+v -> %+v", i, g.Name, c, nc)
			}
			if c.Id != categoryID {
				if stringOf(c.Overrides) != stringOf(nc.Overrides) {
					t.Errorf("unrelated category %s lost its overrides", c.Name)
				}
			}
		}
	}

	// The rewrite touches exactly one category's amount_changes. Line by line
	// that is at most: the category's amount line gains or loses its trailing
	// comma (one edit), plus the amount_changes line added, edited or dropped.
	// So relative to the original, at most two lines disappear and at most
	// two appear; everything else survives verbatim and in order. An LCS
	// check states exactly that, and anything else means the file was
	// reformatted.
	oldLines, newLines := strings.Split(before, "\n"), strings.Split(after, "\n")
	common := lcsLen(oldLines, newLines)
	if deleted := len(oldLines) - common; deleted > 2 {
		t.Errorf("%d original lines did not survive (want at most 2) — the file was reformatted:\n%s", deleted, after)
	}
	if added := len(newLines) - common; added > 2 {
		t.Errorf("%d new lines appeared (want at most 2) — the file was reformatted:\n%s", added, after)
	}
}

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	return dp[0][0]
}

func findGroup(bf budgetdata.BudgetFile, name string) (budgetdata.Group, bool) {
	for _, g := range bf.Groups {
		if g.Name == name {
			return g, true
		}
	}
	return budgetdata.Group{}, false
}

func stringOf(ovs []budgetdata.Override) string {
	return jsonMustMarshal(ovs)
}

func jsonMustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(b)
}

func f64Ptr(v float64) *float64 { return &v }

func derefPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
