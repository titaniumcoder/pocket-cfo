package tracker

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/accountsdata"
)

const testAccountsJSON = `{
  "accounts": [
    { "name": "Private Checking", "balance": 2000, "as_of": "2026-07-31" }
  ]
}`

func newTestAccounts(t *testing.T, body string) *Accounts {
	t.Helper()
	return &Accounts{FS: fstest.MapFS{accountsPath: &fstest.MapFile{Data: []byte(body)}}}
}

// TestSnapshotOpensTheMonthAfterAsOf is the timing rule the whole layer
// hangs on: a balance read at the end of July is July's CLOSING figure, so
// it opens August. July must not see it.
func TestSnapshotOpensTheMonthAfterAsOf(t *testing.T) {
	a := newTestAccounts(t, testAccountsJSON)

	snap, ok := a.Snapshot(context.Background())
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.Cents != 200000 {
		t.Errorf("cents = %d, want 200000", snap.Cents)
	}
	if want := (yearMonth{2026, time.August}); snap.OpensMonth != want {
		t.Errorf("OpensMonth = %+v, want %+v (the month after the 31 July read)", snap.OpensMonth, want)
	}
}

// TestSnapshotOpensAcrossYearBoundary covers the December edge of that
// shift, where the opening month lands in the next calendar year.
func TestSnapshotOpensAcrossYearBoundary(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Private","balance":100,"as_of":"2026-12-31"}
	]}`)
	snap, ok := a.Snapshot(context.Background())
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if want := (yearMonth{2027, time.January}); snap.OpensMonth != want {
		t.Errorf("OpensMonth = %+v, want %+v", snap.OpensMonth, want)
	}
}

// TestAccountsMissingFileIsNotAnError pins the optional-layer contract: a
// deployment that upgrades into this feature without writing accounts.json
// must keep working, not start erroring.
func TestAccountsMissingFileIsNotAnError(t *testing.T) {
	a := &Accounts{FS: fstest.MapFS{}}
	af, err := a.File(context.Background())
	if err != nil {
		t.Fatalf("missing accounts.json should not error, got %v", err)
	}
	if len(af.Accounts) != 0 {
		t.Errorf("accounts = %+v, want none", af.Accounts)
	}
	if _, ok := a.Snapshot(context.Background()); ok {
		t.Error("no file should mean no snapshot")
	}
}

func TestAccountsNilIsSafe(t *testing.T) {
	var a *Accounts
	if _, err := a.File(context.Background()); err != nil {
		t.Errorf("nil Accounts should degrade quietly, got %v", err)
	}
	if _, ok := a.Snapshot(context.Background()); ok {
		t.Error("nil Accounts should have no snapshot")
	}
	a.Evict() // must not panic
}

// TestSnapshotForAnchorsAtLatestAsOf covers the documented combining rule:
// accounts of one kind are summed and anchored at the newest as_of among
// them, so a single stale entry can't drag the effective date backwards.
func TestSnapshotForAnchorsAtLatestAsOf(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Old","balance":100,"as_of":"2026-03-31"},
		{"name":"New","balance":250,"as_of":"2026-09-30"}
	]}`)
	snap, ok := a.Snapshot(context.Background())
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.Cents != 35000 {
		t.Errorf("cents = %d, want 35000 (both summed)", snap.Cents)
	}
	if want := (yearMonth{2026, time.October}); snap.OpensMonth != want {
		t.Errorf("OpensMonth = %+v, want %+v (latest as_of wins, shifted a month)", snap.OpensMonth, want)
	}
}

func TestSnapshotForSkipsMalformedDate(t *testing.T) {
	af := accountsdata.AccountsFile{Accounts: []accountsdata.Account{
		{Name: "Bad", Balance: 100, AsOf: "not-a-date"},
		{Name: "Good", Balance: 200, AsOf: "2026-05-31"},
	}}
	snap, ok := snapshotFor(af)
	if !ok {
		t.Fatal("a malformed date should drop that account, not the whole side")
	}
	if snap.Cents != 20000 {
		t.Errorf("cents = %d, want 20000 (malformed entry excluded)", snap.Cents)
	}
}

// accountsTracker builds a Tracker with no tracked/expected income at all
// (no Toggl rows, and every month examined is far in the past so nothing is
// projected), a fixed private expense, and the given accounts file. That
// makes each month's balance delta a known constant, so the roll-forward
// arithmetic below is checked against numbers derived by hand rather than
// against whatever the rest of the pipeline happens to produce.
func accountsTracker(t *testing.T, accountsJSON string) *Tracker {
	t.Helper()
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return `[]`, "", "" },
		projects: `[]`,
	}
	client := b.transport()
	return &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
		Budget: newTestBudget(t, map[string]string{"budget.json": `{"groups":[
			{"name":"Housing","kind":"private","categories":[{"name":"Rent","amount":1000}]}
		]}`}),
		Accounts: newTestAccounts(t, accountsJSON),
		Personal: PersonalParams{
			EmployerRate: 0.1892, EmployeeRate: 0.1378,
			MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10,
		},
	}
}

// TestPrivateBalanceRollsForwardAndCompounds is the core money-math test for
// the account layer, walking the exact scenario the feature was specified
// against: 2 000 read at the end of July, no income, 1 000/month of private
// expenses.
//
//	June, July — before the balance opens; no layer at all
//	Aug        — opens at 2 000 (July's closing figure), closes at 1 000
//	Sept       — opens at Aug's close (1 000), closes at 0
//	Oct        — opens at 0, closes at -1 000
//
// Two things are being pinned. The month shift: a balance read 31 July is
// August's opening figure, so July itself must not see it. And the
// compounding: each month opens where the previous closed, so the balance
// genuinely depletes instead of resetting to the snapshot every month.
func TestPrivateBalanceRollsForwardAndCompounds(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","balance":2000,"as_of":"2026-07-31"}
	]}`)

	for _, m := range []time.Month{time.June, time.July} {
		f := trk.ComputeMonth(context.Background(), 2026, m)
		if f.ShowOpeningBalance {
			t.Errorf("%s is before the balance opens — it must show no opening balance", m)
		}
		if f.BalanceCents != -100000 {
			t.Errorf("%s Balance = %d, want -100000 (expenses only, balance ignored)", m, f.BalanceCents)
		}
	}

	tests := []struct {
		month       time.Month
		wantOpening int
		wantBalance int
	}{
		{time.August, 200000, 100000},
		{time.September, 100000, 0},
		{time.October, 0, -100000},
	}
	for _, tt := range tests {
		t.Run(tt.month.String(), func(t *testing.T) {
			f := trk.ComputeMonth(context.Background(), 2026, tt.month)
			if f.AccountsErr != "" {
				t.Fatalf("AccountsErr = %q", f.AccountsErr)
			}
			if !f.ShowOpeningBalance {
				t.Fatal("expected an opening balance from the month the snapshot opens onward")
			}
			if f.OpeningBalanceCents != tt.wantOpening {
				t.Errorf("opening = %d, want %d", f.OpeningBalanceCents, tt.wantOpening)
			}
			if f.BalanceCents != tt.wantBalance {
				t.Errorf("Balance = %d, want %d (opening + net income - private expenses)", f.BalanceCents, tt.wantBalance)
			}
		})
	}
}

// TestPrivateBalanceStaleSnapshotErrors covers the roll-forward cap: a
// balance read years ago has drifted too far to present as this month's
// opening figure, so it reports instead of quietly showing fiction.
func TestPrivateBalanceStaleSnapshotErrors(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Ancient","balance":2000,"as_of":"2020-01-31"}
	]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.AccountsErr == "" {
		t.Error("expected a staleness error for a snapshot far beyond the roll-forward cap")
	}
	if f.ShowOpeningBalance {
		t.Error("a stale snapshot must not still render an opening balance")
	}
}

// TestNoAccountsLayerLeavesBalanceUnchanged is the backward-compatibility
// guard: without accounts.json the Balance row must read exactly as it did
// before the feature existed.
func TestNoAccountsLayerLeavesBalanceUnchanged(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.ShowOpeningBalance {
		t.Error("an empty accounts file must show no balance rows")
	}
	if f.BalanceCents != f.FundingPersonal.NetIncomeCents-f.PrivateTotalSpentCents {
		t.Errorf("Balance = %d, want net income - private expenses with no accounts layer", f.BalanceCents)
	}
}

func TestSnapshotForNegativeBalance(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Overdrawn","balance":-150.5,"as_of":"2026-05-31"}
	]}`)
	snap, ok := a.Snapshot(context.Background())
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.Cents != -15050 {
		t.Errorf("cents = %d, want -15050", snap.Cents)
	}
}
