package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/actualsdata"
)

func strp(s string) *string { return &s }

func addTx(id, date string, amount float64, category string) actualsdata.Transaction {
	return actualsdata.Transaction{
		Id: id, Date: date, Description: "LINE " + id, Amount: amount,
		Account: "A", Category: strp(category),
	}
}

func cover(from, to string) actualsdata.Coverage {
	return actualsdata.Coverage{Account: "A", From: from, To: to, ImportedAt: "2026-09-01"}
}

func add(t *testing.T, s *Service, req AddRequest) (*AddResult, error) {
	t.Helper()
	return s.AddTransactions(context.Background(), req)
}

// committed decodes whatever the fake ended up holding for a path.
func committed(t *testing.T, gh *fakeGitHub, path string) actualsdata.ActualsFile {
	t.Helper()
	raw, ok := gh.files[path]
	if !ok {
		t.Fatalf("%s was never committed", path)
	}
	var out actualsdata.ActualsFile
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s does not parse: %v", path, err)
	}
	return out
}

// TestAddFilesEachLineByItsOwnDate: the caller sends what it read off a
// statement and never says which month anything belongs to. A statement
// crossing a boundary is one call.
func TestAddFilesEachLineByItsOwnDate(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)

	got, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{
			addTx("a1", "2026-08-30", 900, idRent),
			addTx("s1", "2026-09-02", 40, idGroceries),
			addTx("a2", "2026-08-31", 20, idGroceries),
		},
		Coverage: []actualsdata.Coverage{
			cover("2026-08-01", "2026-08-31"),
			{Account: "A", From: "2026-09-01", To: "2026-09-30", ImportedAt: "2026-10-01"},
		},
	})
	if err != nil {
		t.Fatalf("AddTransactions: %v", err)
	}
	if got.Added != 3 {
		t.Errorf("Added = %d, want 3", got.Added)
	}
	if len(got.Months) != 2 || got.Months[0].Month != "2026-08" || got.Months[1].Month != "2026-09" {
		t.Fatalf("months = %+v, want August then September", got.Months)
	}
	if got.Months[0].Added != 2 || got.Months[1].Added != 1 {
		t.Errorf("per-month counts = %+v, want 2 then 1", got.Months)
	}

	aug := committed(t, gh, augustPath)
	if aug.Month != "2026-08" || len(aug.Transactions) != 2 {
		t.Errorf("August = %+v", aug)
	}
	sep := committed(t, gh, "data/actuals/2026-09.json")
	if sep.Month != "2026-09" || len(sep.Transactions) != 1 {
		t.Errorf("September = %+v", sep)
	}
}

// TestAddNeverRemovesOrReordersWhatIsAlreadyThere is the guarantee the whole
// endpoint exists for. A reviewer must be able to see in one glance that the
// commit touched nothing that was already recorded — which is why new lines go
// at the end instead of being sorted into place.
func TestAddNeverRemovesOrReordersWhatIsAlreadyThere(t *testing.T) {
	gh := newFakeGitHub(map[string]string{
		augustPath: doc(
			tx("old3", "2026-08-20", "30", idGroceries),
			tx("old1", "2026-08-02", "900", idRent),
			tx("old2", "2026-08-11", "10", idGroceries),
		),
	})
	s := writeService(t, gh)

	if _, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{addTx("new1", "2026-08-05", 25, idGroceries)},
	}); err != nil {
		t.Fatalf("AddTransactions: %v", err)
	}

	aug := committed(t, gh, augustPath)
	if len(aug.Transactions) != 4 {
		t.Fatalf("got %d transactions, want the 3 that were there plus 1", len(aug.Transactions))
	}
	var ids []string
	for _, tx := range aug.Transactions {
		ids = append(ids, tx.Id)
	}
	if strings.Join(ids, ",") != "old3,old1,old2,new1" {
		t.Errorf("ids = %v, want the recorded order untouched and the new line appended", ids)
	}
}

// TestAddIsIdempotent: ids are derived from the statement line, which only
// helps if re-sending a statement is a no-op rather than a duplicate — or,
// worse, an empty commit and a redeploy.
func TestAddIsIdempotent(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	s := writeService(t, gh)

	got, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{addTx("t1", "2026-08-01", 900, idRent)},
		Coverage:     []actualsdata.Coverage{cover("2026-08-01", "2026-08-31")},
	})
	if err != nil {
		t.Fatalf("AddTransactions: %v", err)
	}
	if got.Added != 0 || len(got.Skipped) != 1 || got.Skipped[0] != "t1" {
		t.Errorf("result = %+v, want nothing added and t1 skipped", got)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0 — an unchanged month must not redeploy", gh.puts)
	}
	if got.DeployPending {
		t.Error("DeployPending on a no-op")
	}
}

// TestAddRefusesToRewriteARecordedLine: same id, different content is either a
// mistake or a change of mind, and quietly keeping one of the two versions
// would be a lie either way.
func TestAddRefusesToRewriteARecordedLine(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	s := writeService(t, gh)

	_, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{addTx("t1", "2026-08-01", 900, idGroceries)},
	})
	if err == nil {
		t.Fatal("accepted a conflicting rewrite")
	}
	if e, ok := err.(*Error); !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "edit_transactions") {
		t.Errorf("err = %v, want it to name the tool that can do this", err)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0", gh.puts)
	}
}

// TestAddValidatesEveryMonthBeforeCommittingAny: a batch that is wrong about
// its second month must not leave the first one written.
func TestAddValidatesEveryMonthBeforeCommittingAny(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)

	_, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{
			addTx("a1", "2026-08-30", 900, idRent),
			addTx("s1", "2026-09-02", 40, "not-a-category"),
		},
		Coverage: []actualsdata.Coverage{
			cover("2026-08-01", "2026-08-31"),
			{Account: "A", From: "2026-09-01", To: "2026-09-30", ImportedAt: "2026-10-01"},
		},
	})
	if err == nil {
		t.Fatal("accepted an unknown category")
	}
	if e, ok := err.(*Error); !ok || e.Code != CodeValidationFailed {
		t.Fatalf("err = %v, want validation_failed", err)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0 — August must not land when September is bad", gh.puts)
	}
}

// TestAddNeedsCoverageForAMonthItInvents: a month that has never been
// reconciled cannot be created without saying which days were read, and the
// caller is told that in those words rather than through a schema error.
func TestAddNeedsCoverageForAMonthItInvents(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)

	_, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{addTx("a1", "2026-08-30", 900, idRent)},
	})
	if err == nil {
		t.Fatal("created a month with no coverage")
	}
	if e, ok := err.(*Error); !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want invalid_request", err)
	}
	if !strings.Contains(err.Error(), "coverage") {
		t.Errorf("err = %v, want it to say what is missing", err)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0", gh.puts)
	}
}

// TestAddExtendsCoverageWithoutLosingRanges: a re-import that read further
// into the month replaces its own range and leaves every other account alone.
func TestAddExtendsCoverageWithoutLosingRanges(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: `{
  "month": "2026-08",
  "coverage": [
    { "account": "A", "from": "2026-08-01", "to": "2026-08-09", "imported_at": "2026-08-10" },
    { "account": "B", "from": "2026-08-01", "to": "2026-08-31", "imported_at": "2026-09-01" }
  ],
  "transactions": [` + tx("t1", "2026-08-01", "900", idRent) + `]
}`})
	s := writeService(t, gh)

	got, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{addTx("t2", "2026-08-20", 30, idGroceries)},
		Coverage:     []actualsdata.Coverage{cover("2026-08-01", "2026-08-31")},
	})
	if err != nil {
		t.Fatalf("AddTransactions: %v", err)
	}
	if got.Added != 1 {
		t.Errorf("Added = %d, want 1", got.Added)
	}

	aug := committed(t, gh, augustPath)
	if len(aug.Coverage) != 2 {
		t.Fatalf("coverage = %+v, want the two accounts", aug.Coverage)
	}
	if aug.Coverage[0].Account != "A" || aug.Coverage[0].To != "2026-08-31" {
		t.Errorf("A = %+v, want it extended to the 31st in place", aug.Coverage[0])
	}
	if aug.Coverage[1].Account != "B" || aug.Coverage[1].To != "2026-08-31" {
		t.Errorf("B = %+v, want it untouched", aug.Coverage[1])
	}
}

// TestAddRefusesCoverageAcrossAMonthBoundary: one range cannot describe two
// files, and the error says what to do about it.
func TestAddRefusesCoverageAcrossAMonthBoundary(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)

	_, err := add(t, s, AddRequest{
		Coverage: []actualsdata.Coverage{{Account: "A", From: "2026-08-01", To: "2026-09-15", ImportedAt: "2026-09-16"}},
	})
	if err == nil || !strings.Contains(err.Error(), "crosses a month boundary") {
		t.Fatalf("err = %v, want a refusal naming the problem", err)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0", gh.puts)
	}
}

// TestAddRecordsUntrackedCash: the case that motivated untracked — money out
// of a machine that Hermes cannot place and must not guess at.
func TestAddRecordsUntrackedCash(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)

	_, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{{
			Id: "w1", Date: "2026-08-14", Description: "ATM WITHDRAWAL", Amount: 100, Account: "A",
			Untracked: strp("cash, not spent yet"),
		}},
		Coverage: []actualsdata.Coverage{cover("2026-08-01", "2026-08-31")},
	})
	if err != nil {
		t.Fatalf("AddTransactions: %v", err)
	}
	aug := committed(t, gh, augustPath)
	if len(aug.Transactions) != 1 || aug.Transactions[0].Untracked == nil {
		t.Fatalf("committed = %+v, want the untracked note preserved", aug.Transactions)
	}
	if *aug.Transactions[0].Untracked != "cash, not spent yet" {
		t.Errorf("note = %q", *aug.Transactions[0].Untracked)
	}
}

// TestAddRejectsADuplicateIdWithinOneRequest: two lines claiming the same id
// in one batch means the caller's id derivation collided, and silently keeping
// the last would drop a real statement line.
func TestAddRejectsADuplicateIdWithinOneRequest(t *testing.T) {
	gh := newFakeGitHub(nil)
	s := writeService(t, gh)

	_, err := add(t, s, AddRequest{
		Transactions: []actualsdata.Transaction{
			addTx("t1", "2026-08-01", 900, idRent),
			addTx("t1", "2026-08-02", 30, idGroceries),
		},
		Coverage: []actualsdata.Coverage{cover("2026-08-01", "2026-08-31")},
	})
	if err == nil || !strings.Contains(err.Error(), "appears twice") {
		t.Fatalf("err = %v, want a refusal of the duplicate id", err)
	}
	if gh.puts != 0 {
		t.Errorf("PUT called %d times, want 0", gh.puts)
	}
}

// TestAddNeverShrinksCoverage: a re-import claiming fewer days than are
// already recorded would silently reopen a month the dashboard has stopped
// withholding judgement on — the coverage equivalent of deleting lines.
func TestAddNeverShrinksCoverage(t *testing.T) {
	gh := newFakeGitHub(map[string]string{augustPath: doc(tx("t1", "2026-08-01", "900", idRent))})
	s := writeService(t, gh)

	if _, err := add(t, s, AddRequest{
		Coverage: []actualsdata.Coverage{cover("2026-08-01", "2026-08-09")},
	}); err != nil {
		t.Fatalf("AddTransactions: %v", err)
	}

	aug := committed(t, gh, augustPath)
	if len(aug.Coverage) != 1 || aug.Coverage[0].To != "2026-08-31" {
		t.Errorf("coverage = %+v, want the recorded 31st kept", aug.Coverage)
	}
}

// TestMarshalMonthKeepsSchemaOrder: go-jsonschema emits struct fields
// alphabetically, so marshalling through the generated type would rewrite the
// key order of every line in the file each time one line is appended — and the
// commit a reviewer opens to confirm nothing was destroyed would show
// everything changed. The order here is the order the files already use.
func TestMarshalMonthKeepsSchemaOrder(t *testing.T) {
	body, err := marshalMonth(actualsdata.ActualsFile{
		Schema:   strp("../../internal/finance/data/actuals.schema.json"),
		Month:    "2026-08",
		Coverage: []actualsdata.Coverage{cover("2026-08-01", "2026-08-31")},
		Transactions: []actualsdata.Transaction{
			addTx("t1", "2026-08-01", 900, idRent),
			{Id: "t2", Date: "2026-08-14", Description: "ATM", Amount: 100, Account: "A", Splits: []actualsdata.Split{
				{Amount: 60, Category: strp(idGroceries)},
				{Amount: 40, Untracked: strp("in my wallet")},
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshalMonth: %v", err)
	}
	got := string(body)

	for _, want := range []string{
		`"$schema"`,
		"\"id\": \"t1\",\n      \"date\": \"2026-08-01\",\n      \"description\": \"LINE t1\",\n      \"amount\": 900,\n      \"account\": \"A\",\n      \"category\":",
		"\"amount\": 40,\n          \"untracked\": \"in my wallet\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("committed body is missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("no trailing newline")
	}
	// An unset optional must be absent, not null: the schema forbids unknown
	// and null-valued properties on the way back in.
	if strings.Contains(got, "null") {
		t.Errorf("a null reached the committed body:\n%s", got)
	}
	// And it must survive the round trip through the generated unmarshaller.
	var back actualsdata.ActualsFile
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("committed body does not parse back: %v", err)
	}
	if len(back.Transactions) != 2 || len(back.Transactions[1].Splits) != 2 {
		t.Errorf("round trip lost data: %+v", back)
	}
}
