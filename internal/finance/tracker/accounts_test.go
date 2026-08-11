package tracker

import (
	"context"
	"net/http/httptest"
	"strings"
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
			{"name":"Housing","kind":"private","categories":[{"id":"00000000-0000-4000-8000-000000000001","name":"Rent","amount":1000}]}
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

// TestAvailableIsOpeningPlusNetIncome pins the middle line of the budget
// ledger and its relationship to the two around it: Available is what there
// is before private expenses come off, and Balance is what's left after.
func TestAvailableIsOpeningPlusNetIncome(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","balance":2000,"as_of":"2026-07-31"}
	]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)

	if !f.ShowOpeningBalance {
		t.Fatal("expected an opening balance for the month the snapshot opens")
	}
	if want := f.OpeningBalanceCents + f.FundingPersonal.NetIncomeCents; f.AvailableCents != want {
		t.Errorf("AvailableCents = %d, want %d (opening + net income)", f.AvailableCents, want)
	}
	if want := f.AvailableCents - f.PrivateTotalPlannedCents; f.BalanceCents != want {
		t.Errorf("BalanceCents = %d, want %d (available - private expenses)", f.BalanceCents, want)
	}
}

// TestNegativeBalancesRenderRed guards the money rows that are allowed to
// go negative and used to be painted green regardless — an overdraft
// carried in from last month reading as good news is exactly backwards.
func TestNegativeBalancesRenderRed(t *testing.T) {
	// Opening at 2 000 against 1 000/month of expenses and no income runs
	// out during the third month, so October opens negative.
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","balance":2000,"as_of":"2026-07-31"}
	]}`)

	positive := trk.ComputeMonth(context.Background(), 2026, time.August)
	if positive.OpeningBalanceCents <= 0 {
		t.Fatalf("August opening = %d, want positive for the control case", positive.OpeningBalanceCents)
	}
	recPos := httptest.NewRecorder()
	RenderPage(recPos, positive)
	if strings.Contains(recPos.Body.String(), `class="row net neg"><span class="label">Opening balance`) {
		t.Error("a positive opening balance must not render as negative")
	}

	negative := trk.ComputeMonth(context.Background(), 2026, time.November)
	if negative.OpeningBalanceCents >= 0 {
		t.Fatalf("November opening = %d, want negative for this test to mean anything", negative.OpeningBalanceCents)
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, negative)
	body := rec.Body.String()
	if !strings.Contains(body, `class="row net neg"><span class="label">Opening balance`) {
		t.Errorf("a negative opening balance should carry the neg class, got: %s", body)
	}
	if negative.AvailableCents < 0 && !strings.Contains(body, `neg"><span class="label">Available to spend`) {
		t.Error("a negative Available to spend should carry the neg class too")
	}
}

// TestAvailableHiddenWithoutAnOpeningBalance keeps the row from restating
// Net income when there's no balance layer to add to it.
func TestAvailableHiddenWithoutAnOpeningBalance(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.ShowOpeningBalance {
		t.Fatal("no accounts configured — expected no opening balance")
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	if strings.Contains(rec.Body.String(), "Available to spend") {
		t.Error("Available row should not render without an opening balance to add")
	}
}

// TestStaleDaysThreshold covers the nag's boundary in both directions,
// measured against real current time rather than the month being viewed.
func TestStaleDaysThreshold(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		asOf      time.Time
		wantDays  int
		wantStale bool
	}{
		{"read today", now, 0, false},
		{"just inside the threshold", now.AddDate(0, 0, -staleAfterDays), staleAfterDays, false},
		{"one day past", now.AddDate(0, 0, -(staleAfterDays + 1)), staleAfterDays + 1, true},
		{"badly overdue", now.AddDate(0, 0, -120), 120, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			days, stale := AccountSnapshot{LatestAsOf: tt.asOf}.StaleDays(now)
			if days != tt.wantDays || stale != tt.wantStale {
				t.Errorf("StaleDays = %d, %v; want %d, %v", days, stale, tt.wantDays, tt.wantStale)
			}
		})
	}
}

// TestStaleNoteAppearsOnlyWhenOverdue wires the nag through a real compute:
// a balance read moments ago says nothing, a long-stale one speaks up. The
// snapshot dates are relative to real now, since staleness is measured
// against the calendar rather than the viewed month.
func TestStaleNoteAppearsOnlyWhenOverdue(t *testing.T) {
	now := time.Now()
	viewed := yearMonth{now.Year(), now.Month()}
	// Anchor each snapshot to the end of the month before the viewed one,
	// so the balance opens the month under test either way.
	opensPrev := viewed.addMonths(-1)

	fresh := time.Date(opensPrev.Year, opensPrev.Month, 28, 0, 0, 0, 0, time.UTC)
	if now.Sub(fresh).Hours()/24 > staleAfterDays {
		t.Skip("running late enough in the month that even last month's read is already stale")
	}
	trkFresh := accountsTracker(t, `{"accounts":[{"name":"P","balance":2000,"as_of":"`+fresh.Format("2006-01-02")+`"}]}`)
	f := trkFresh.ComputeMonth(context.Background(), viewed.Year, viewed.Month)
	if f.AccountsStaleNote != "" {
		t.Errorf("a recently read balance should not nag, got %q", f.AccountsStaleNote)
	}

	old := now.AddDate(0, 0, -100)
	trkOld := accountsTracker(t, `{"accounts":[{"name":"P","balance":2000,"as_of":"`+old.Format("2006-01-02")+`"}]}`)
	fOld := trkOld.ComputeMonth(context.Background(), viewed.Year, viewed.Month)
	if fOld.AccountsStaleNote == "" {
		t.Error("a balance read 100 days ago should nag")
	}
}

// TestVeryStaleBalanceStillComputes is the point of the whole layer: going
// a long time without reading your bank makes the figure *nag*, never makes
// it disappear. A balance last read years ago is still carried forward and
// still shown — with the note attached.
func TestVeryStaleBalanceStillComputes(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Neglected","balance":2000,"as_of":"2024-01-31"}
	]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)

	if f.AccountsErr != "" {
		t.Fatalf("AccountsErr = %q — a stale balance must still be carried, not withheld", f.AccountsErr)
	}
	if !f.ShowOpeningBalance {
		t.Error("expected the opening balance to still render for a long-stale snapshot")
	}
	if f.AccountsStaleNote == "" {
		t.Error("expected the staleness note on a years-old balance")
	}
}

// TestImplausibleAsOfDateErrors covers the one case that still refuses: a
// mistyped year, which would otherwise fan out into an external API call
// per calendar year spanned.
func TestImplausibleAsOfDateErrors(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Typo","balance":2000,"as_of":"1999-01-31"}
	]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.AccountsErr == "" {
		t.Error("expected an error for an as_of date decades in the past")
	}
	if f.ShowOpeningBalance {
		t.Error("an implausible date must not still render an opening balance")
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
	if f.BalanceCents != f.FundingPersonal.NetIncomeCents-f.PrivateTotalPlannedCents {
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
