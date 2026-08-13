package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

const accountsFilePath = "data/accounts.json"

// Hand-formatted the way the file actually is in a data repo: two accounts,
// one reading each on a single line, a note on one of them.
const writeAccountsJSON = `{
  "$schema": "../internal/finance/data/accounts.schema.json",
  "accounts": [
    {
      "name": "Private Checking",
      "kind": "private",
      "balances": [
        { "as_of": "2026-07-31", "balance": 4200, "note": "Read at month end; opens August" }
      ]
    },
    {
      "name": "Company Checking",
      "kind": "company",
      "balances": [
        { "as_of": "2026-04-30", "balance": 5100 },
        { "as_of": "2026-07-31", "balance": 6800 }
      ]
    }
  ]
}
`

func balanceService(t *testing.T, gh *fakeGitHub) *Service {
	t.Helper()
	srv := gh.server(t)
	return &Service{
		Accounts: &tracker.Accounts{FS: fstest.MapFS{
			"accounts.json": &fstest.MapFile{Data: []byte(writeAccountsJSON)},
		}},
		Store: &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
		Now:   func() time.Time { return time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC) },
	}
}

func recordBalance(t *testing.T, s *Service, req RecordBalanceRequest) (*RecordBalanceResult, error) {
	t.Helper()
	return s.RecordAccountBalance(context.Background(), req)
}

func accountsAfter(t *testing.T, gh *fakeGitHub) string {
	t.Helper()
	return string(gh.files[accountsFilePath])
}

func newBalanceGitHub() *fakeGitHub {
	return newFakeGitHub(map[string]string{accountsFilePath: writeAccountsJSON})
}

// TestARecordedBalanceIsAppendedNotWrittenOver is the whole contract of the
// file in one test: the old reading survives untouched, the new one lands
// after it, and everything else in the document is byte-identical.
func TestARecordedBalanceIsAppendedNotWrittenOver(t *testing.T) {
	gh := newBalanceGitHub()
	s := balanceService(t, gh)

	got, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Private Checking", AsOf: "2026-08-31", Balance: 3875.4,
		Note: "read on the app",
	})
	if err != nil {
		t.Fatalf("RecordAccountBalance: %v", err)
	}
	if got.Opens != "2026-09" || got.Closes != "2026-08" {
		t.Errorf("closes/opens = %q/%q, want 2026-08/2026-09 — a month-end reading opens the NEXT month", got.Closes, got.Opens)
	}
	if got.Kind != "private" || got.Readings != 2 || !got.DeployPending {
		t.Errorf("result = %+v", got)
	}

	after := accountsAfter(t, gh)
	if !strings.Contains(after, `{ "as_of": "2026-07-31", "balance": 4200, "note": "Read at month end; opens August" }`) {
		t.Errorf("the earlier reading was reformatted or lost:\n%s", after)
	}
	if !strings.Contains(after, `{ "as_of": "2026-08-31", "balance": 3875.4, "note": "read on the app" }`) {
		t.Errorf("the new reading is not in the file as written:\n%s", after)
	}
	// The one line added is the only line that changed: everything the write
	// did not touch stays formatted exactly as the human left it.
	if want := strings.Count(writeAccountsJSON, "\n") + 1; strings.Count(after, "\n") != want {
		t.Errorf("the file went from %d lines to %d — a whole-file reformat, not an append:\n%s",
			strings.Count(writeAccountsJSON, "\n"), strings.Count(after, "\n"), after)
	}
	if !strings.Contains(gh.lastMsg, "Private Checking") || !strings.Contains(gh.lastMsg, "read on the app") {
		t.Errorf("commit message = %q, want the account and the note", gh.lastMsg)
	}
}

// TestASecondAccountKeepsItsOwnHistory: the append has to find the right
// account among several, and leave the others alone.
func TestASecondAccountKeepsItsOwnHistory(t *testing.T) {
	gh := newBalanceGitHub()
	s := balanceService(t, gh)

	if _, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Company Checking", AsOf: "2026-08-31", Balance: 7150,
	}); err != nil {
		t.Fatalf("RecordAccountBalance: %v", err)
	}

	var af struct {
		Accounts []struct {
			Name     string `json:"name"`
			Balances []struct {
				AsOf    string  `json:"as_of"`
				Balance float64 `json:"balance"`
			} `json:"balances"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(accountsAfter(t, gh)), &af); err != nil {
		t.Fatalf("the written file does not parse: %v", err)
	}
	for _, a := range af.Accounts {
		switch a.Name {
		case "Company Checking":
			if len(a.Balances) != 3 || a.Balances[2].Balance != 7150 {
				t.Errorf("Company Checking = %+v, want the new reading appended last", a.Balances)
			}
		case "Private Checking":
			if len(a.Balances) != 1 {
				t.Errorf("Private Checking grew to %d readings; it was not the account written", len(a.Balances))
			}
		}
	}
}

// TestAMidMonthBalanceIsRefused is the rule the tool exists to hold: a
// balance is an end-of-day figure for the last day of a month. Anything else
// would open the next month with money that had not been spent yet.
func TestAMidMonthBalanceIsRefused(t *testing.T) {
	for _, asOf := range []string{"2026-08-01", "2026-08-15", "2026-08-30", "2026-02-27"} {
		t.Run(asOf, func(t *testing.T) {
			gh := newBalanceGitHub()
			s := balanceService(t, gh)
			_, err := recordBalance(t, s, RecordBalanceRequest{
				Account: "Private Checking", AsOf: asOf, Balance: 100,
			})
			e, ok := err.(*Error)
			if !ok || e.Code != CodeInvalidRequest {
				t.Fatalf("err = %v, want an invalid_request", err)
			}
			if !strings.Contains(e.Message, "mid-month") {
				t.Errorf("message = %q, want it to say why", e.Message)
			}
			if gh.puts != 0 {
				t.Error("a mid-month balance was committed")
			}
		})
	}
}

// TestTheLastDayOfEveryMonthIsAccepted keeps the other half honest: February
// in a leap year, and the 30-day months, are month ends too.
func TestTheLastDayOfEveryMonthIsAccepted(t *testing.T) {
	for _, asOf := range []string{"2026-01-31", "2026-02-28", "2024-02-29", "2026-04-30", "2026-06-30", "2026-12-31"} {
		t.Run(asOf, func(t *testing.T) {
			if err := refuseAMidMonthReading(asOf); err != nil {
				t.Errorf("%s was refused: %v", asOf, err)
			}
		})
	}
}

// TestABalanceFromTheFutureIsRefused: a month that has not ended cannot have
// been read off the bank, so accepting one would be recording a projection as
// a fact — and it would anchor every later month to it.
func TestABalanceFromTheFutureIsRefused(t *testing.T) {
	gh := newBalanceGitHub()
	s := balanceService(t, gh) // "today" is 2026-09-03
	_, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Private Checking", AsOf: "2026-09-30", Balance: 100,
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want an invalid_request", err)
	}
	if !strings.Contains(e.Message, "2026-08-31") {
		t.Errorf("message = %q, want it to name the last month that can be closed", e.Message)
	}
	if gh.puts != 0 {
		t.Error("a balance for a month that has not ended was committed")
	}
}

// TestTheMonthEndingTodayCanBeRecorded: the boundary the future check must
// not overshoot — on 31 August, August is closed.
func TestTheMonthEndingTodayCanBeRecorded(t *testing.T) {
	gh := newBalanceGitHub()
	s := balanceService(t, gh)
	s.Now = func() time.Time { return time.Date(2026, time.August, 31, 18, 30, 0, 0, time.UTC) }

	if _, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Private Checking", AsOf: "2026-08-31", Balance: 3875,
	}); err != nil {
		t.Fatalf("the month ending today was refused: %v", err)
	}
}

// TestASecondReadingInAMonthIsRefused: two readings in one month are two
// candidate openings with nothing to choose between them, and a correction
// would mean writing over a recorded figure — which nothing here does.
func TestASecondReadingInAMonthIsRefused(t *testing.T) {
	gh := newBalanceGitHub()
	s := balanceService(t, gh)

	_, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Private Checking", AsOf: "2026-07-31", Balance: 4500,
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if !strings.Contains(e.Message, "4200") {
		t.Errorf("message = %q, want the figure already recorded", e.Message)
	}
	if gh.puts != 0 {
		t.Error("a second reading for July was committed")
	}
}

// TestAnUnknownAccountIsRefusedWithTheKnownOnes: an account carries the pot
// its money is in, which decides which side of the payroll cascade a figure
// lands on. Creating one on a name the agent invented would be guessing that.
func TestAnUnknownAccountIsRefusedWithTheKnownOnes(t *testing.T) {
	gh := newBalanceGitHub()
	s := balanceService(t, gh)

	_, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Savings", AsOf: "2026-08-31", Balance: 100,
	})
	e, ok := err.(*Error)
	if !ok || e.Code != CodeInvalidRequest {
		t.Fatalf("err = %v, want an invalid_request", err)
	}
	details, _ := e.Details.(map[string]any)
	known, _ := details["known_accounts"].([]string)
	if len(known) != 2 {
		t.Errorf("details = %v, want the accounts that do exist", e.Details)
	}
	if gh.puts != 0 {
		t.Error("an unknown account was committed")
	}
}

func TestARecordedBalanceRequiresAnAccountAndARealDate(t *testing.T) {
	tests := []struct {
		name string
		req  RecordBalanceRequest
	}{
		{"no account", RecordBalanceRequest{AsOf: "2026-08-31", Balance: 1}},
		{"blank account", RecordBalanceRequest{Account: "  ", AsOf: "2026-08-31", Balance: 1}},
		{"no date", RecordBalanceRequest{Account: "Private Checking", Balance: 1}},
		{"a month, not a day", RecordBalanceRequest{Account: "Private Checking", AsOf: "2026-08", Balance: 1}},
		{"not a real date", RecordBalanceRequest{Account: "Private Checking", AsOf: "2026-02-31", Balance: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newBalanceGitHub()
			s := balanceService(t, gh)
			_, err := recordBalance(t, s, tt.req)
			if e, ok := err.(*Error); !ok || e.Code != CodeInvalidRequest {
				t.Fatalf("err = %v, want an invalid_request", err)
			}
			if gh.puts != 0 {
				t.Error("it was committed anyway")
			}
		})
	}
}

// TestAZeroOrNegativeBalanceIsARealReading: an emptied account and an
// overdrawn one are facts about the bank, not input errors.
func TestAZeroOrNegativeBalanceIsARealReading(t *testing.T) {
	for _, balance := range []float64{0, -150.5} {
		gh := newBalanceGitHub()
		s := balanceService(t, gh)
		got, err := recordBalance(t, s, RecordBalanceRequest{
			Account: "Private Checking", AsOf: "2026-08-31", Balance: balance,
		})
		if err != nil {
			t.Fatalf("balance %v was refused: %v", balance, err)
		}
		if got.Balance != balance {
			t.Errorf("balance = %v, want %v", got.Balance, balance)
		}
	}
}

// TestRecordingABalanceWithoutAStoreIsNotConfigured: no write token means no
// commit, and the honest answer is that the surface exists but cannot write —
// not a 500.
func TestRecordingABalanceWithoutAStoreIsNotConfigured(t *testing.T) {
	s := newService(t)
	_, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Private Checking", AsOf: "2026-06-30", Balance: 1,
	})
	if e, ok := err.(*Error); !ok || e.Code != CodeWriteNotConfigured {
		t.Fatalf("err = %v, want write_not_configured", err)
	}
}

// TestARecordedBalanceIsVisibleToTheDashboardImmediately: the commit takes
// minutes to deploy, and the figure is wanted now — it is the opening balance
// of the month the user is looking at.
func TestARecordedBalanceIsVisibleToTheDashboardImmediately(t *testing.T) {
	gh := newBalanceGitHub()
	s := balanceService(t, gh)
	ctx := context.Background()

	before, err := s.AccountsList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before[1].AsOf != "2026-07-31" {
		t.Fatalf("as_of = %q before the write; this test would prove nothing", before[1].AsOf)
	}

	if _, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Private Checking", AsOf: "2026-08-31", Balance: 3875,
	}); err != nil {
		t.Fatal(err)
	}

	after, err := s.AccountsList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after {
		if a.Name == "Private Checking" && a.AsOf != "2026-08-31" {
			t.Errorf("as_of = %q right after the write, want 2026-08-31", a.AsOf)
		}
	}
}

// TestAConflictingWriteIsNotSwallowed: another writer between the read and
// the put must surface as a conflict rather than as a silent overwrite.
func TestAConflictingWriteIsNotSwallowed(t *testing.T) {
	gh := newBalanceGitHub()
	gh.conflict = true
	s := balanceService(t, gh)

	_, err := recordBalance(t, s, RecordBalanceRequest{
		Account: "Private Checking", AsOf: "2026-08-31", Balance: 3875,
	})
	if e, ok := err.(*Error); !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
}

// TestAnAppendIsRefusedIfItWouldChangeAnythingElse: the write checks its own
// output before committing rather than trusting itself, exactly as the two
// actuals writes do.
func TestAnAppendIsRefusedIfItWouldChangeAnythingElse(t *testing.T) {
	const before = `{"accounts":[{"name":"A","kind":"private","balances":[{"as_of":"2026-07-31","balance":10}]}]}`
	tests := map[string]string{
		"a reading vanished":  `{"accounts":[{"name":"A","kind":"private","balances":[{"as_of":"2026-08-31","balance":20}]}]}`,
		"a figure was edited": `{"accounts":[{"name":"A","kind":"private","balances":[{"as_of":"2026-07-31","balance":99},{"as_of":"2026-08-31","balance":20}]}]}`,
		"an account vanished": `{"accounts":[]}`,
	}
	reading := accountsdata.Reading{AsOf: "2026-08-31", Balance: 20}
	for name, after := range tests {
		t.Run(name, func(t *testing.T) {
			if err := verifyOnlyTheReadingWasAdded([]byte(before), []byte(after), "A", reading); err == nil {
				t.Error("the check accepted it")
			}
		})
	}
	good := `{"accounts":[{"name":"A","kind":"private","balances":[{"as_of":"2026-07-31","balance":10},{"as_of":"2026-08-31","balance":20}]}]}`
	if err := verifyOnlyTheReadingWasAdded([]byte(before), []byte(good), "A", reading); err != nil {
		t.Errorf("a clean append was refused: %v — the check proves nothing if it refuses everything", err)
	}
}
