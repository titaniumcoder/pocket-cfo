package api

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func testService(t *testing.T) *Service {
	t.Helper()
	legislation, err := tracker.ParseLegislation([]tracker.LegislationEntry{{
		From:      "2026-01",
		IncomeTax: &tracker.TaxEntry{Bands: []tracker.BandEntry{{From: 0, Rate: rate(0.10)}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		Budget: &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(`{"groups":[
			{"name":"Housing","kind":"private","categories":[{"id":"` + idRent + `","name":"Rent","amount":900}]}
		]}`)}}},
		Personal: tracker.PersonalParams{Legislation: legislation},
		Now:      func() time.Time { return time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC) },
	}
}

func rate(v float64) *float64 { return &v }

// TestGetFinanceConfigNamesTheRates, and says it cannot be written
// here — otherwise the first thing an agent does with a rate it disagrees with
// is look for the tool to change it.
func TestGetFinanceConfigNamesTheRates(t *testing.T) {
	cfg, err := testService(t).FinanceConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Legislation) != 1 {
		t.Fatalf("got %d periods, want the one configured", len(cfg.Legislation))
	}
	if cfg.Legislation[0].From != "2026-01" {
		t.Errorf("from = %q, want it spelled as config.json spells it", cfg.Legislation[0].From)
	}
	if !strings.Contains(cfg.Legislation[0].InForce, "tax 10%") {
		t.Errorf("the period %q never names its rate", cfg.Legislation[0].InForce)
	}
	if !strings.Contains(cfg.ReadOnly, "never through this API") {
		t.Errorf("read_only = %q, want it to say plainly that this cannot be written here", cfg.ReadOnly)
	}
}

// TestGetDirectorLoanAgreesWithTheDashboardFigure: it answers from the very
// computation the page renders, so the two cannot drift.
func TestGetDirectorLoanAgreesWithTheDashboardFigure(t *testing.T) {
	want := tracker.Figures{
		ShowDirectorLoan: true,
		LoanOpeningCents: 1240000, LoanNetIncomeCents: 493100,
		LoanMovementCents: -500000, LoanClosingCents: 1233100,
	}
	s := &Service{Figures: func(context.Context, int, time.Month) (tracker.Figures, error) { return want, nil }}

	got, err := s.DirectorLoanFor(context.Background(), "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Known {
		t.Fatal("a known loan is reported as unknown")
	}
	if got.OpeningCents != want.LoanOpeningCents || got.ClosingCents != want.LoanClosingCents ||
		got.NetIncomeCents != want.LoanNetIncomeCents || got.MovementCents != want.LoanMovementCents {
		t.Errorf("reported %+v, want the dashboard's own figures", got)
	}
	if got.OpeningCents+got.NetIncomeCents+got.MovementCents != got.ClosingCents {
		t.Error("the reported figures do not add up to the closing one")
	}
	if !strings.Contains(got.PositiveMeans, "company owes the owner") {
		t.Errorf("positive_means = %q, want the sign explained", got.PositiveMeans)
	}
}

// TestGetDirectorLoanSaysUnknownRatherThanZero: "nobody has said" and "nothing
// is owed" are different answers, and an agent acting on the second when the
// first is true would be acting on a number nobody stated.
func TestGetDirectorLoanSaysUnknownRatherThanZero(t *testing.T) {
	s := &Service{Figures: func(context.Context, int, time.Month) (tracker.Figures, error) {
		return tracker.Figures{ShowDirectorLoan: true, DirectorLoanUnknown: "No opening figure is stated before this month"}, nil
	}}

	got, err := s.DirectorLoanFor(context.Background(), "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if got.Known {
		t.Error("an unstated loan is reported as known")
	}
	if got.Unknown == "" {
		t.Error("nothing says why there is no figure")
	}
	if got.ClosingCents != 0 || got.OpeningCents != 0 {
		t.Errorf("an unknown loan carries figures: %+v", got)
	}
}

// TestListAccountsDeclaresTheLoanAsNotAStatementAccount: the loan sits with the
// accounts on the page now, so Hermes meets it there. Told nothing, it would
// read a bank account nobody has ever imported and go looking for statements
// that do not exist.
func TestListAccountsDeclaresTheLoanAsNotAStatementAccount(t *testing.T) {
	s := &Service{Accounts: newAccountsFS(t, `{"accounts":[
		{"name":"Company Checking","kind":"company","balances":[{"as_of":"2026-07-31","balance":204}]}
	],"director_loan":{"balances":[{"as_of":"2026-07-31","balance":-17000}]}}`)}

	got, err := s.AccountsList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var loan *Account
	for i := range got {
		if got[i].Kind == KindDirectorLoan {
			loan = &got[i]
		}
	}
	if loan == nil {
		t.Fatal("list_accounts does not mention the director's loan at all")
	}
	if loan.Kind == "company" || loan.Kind == "private" {
		t.Errorf("the loan is reported as one of the two pots (%q) — it is neither", loan.Kind)
	}
	for _, want := range []string{"Not a bank account", "record_account_balance does not accept it", "get_director_loan"} {
		if !strings.Contains(loan.Note, want) {
			t.Errorf("the note %q never says %q", loan.Note, want)
		}
	}
	// A file that states no loan mentions none.
	bare := &Service{Accounts: newAccountsFS(t, `{"accounts":[
		{"name":"Company Checking","kind":"company","balances":[{"as_of":"2026-07-31","balance":204}]}]}`)}
	only, err := bare.AccountsList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 {
		t.Errorf("a file with no loan reports %d entries, want just the account", len(only))
	}
}

// TestRecordingABalanceForTheLoanIsRefusedWithTheReasonWhy: "no such account"
// reads as a typo and invites a retry; saying what the loan is stops it.
func TestRecordingABalanceForTheLoanIsRefusedWithTheReasonWhy(t *testing.T) {
	s := &Service{Accounts: newAccountsFS(t, `{"accounts":[
		{"name":"Company Checking","kind":"company","balances":[{"as_of":"2026-07-31","balance":204}]}
	],"director_loan":{"balances":[{"as_of":"2026-07-31","balance":-17000}]}}`)}

	_, err := s.RecordAccountBalance(context.Background(), RecordBalanceRequest{
		Account: "Director's loan", AsOf: "2026-08-31", Balance: -12000,
	})
	if err == nil {
		t.Fatal("a balance was accepted for the director's loan")
	}
	for _, want := range []string{"not a bank account", "accounts.json", "get_director_loan"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to say %q", err, want)
		}
	}
}

func newAccountsFS(t *testing.T, body string) *tracker.Accounts {
	t.Helper()
	return &tracker.Accounts{FS: fstest.MapFS{"accounts.json": &fstest.MapFile{Data: []byte(body)}}}
}
