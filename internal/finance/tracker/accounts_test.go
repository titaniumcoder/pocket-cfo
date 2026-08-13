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
    {"name":"Private Checking","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]}
  ]
}`

func newTestAccounts(t *testing.T, body string) *Accounts {
	t.Helper()
	return &Accounts{FS: fstest.MapFS{accountsPath: &fstest.MapFile{Data: []byte(body)}}}
}

func snapshotAt(t *testing.T, a *Accounts, viewed yearMonth) (AccountSnapshot, bool) {
	t.Helper()
	snap, ok, err := a.Snapshot(context.Background(), viewed)
	if err != nil {
		t.Fatalf("Snapshot(%v): %v", viewed, err)
	}
	return snap, ok
}

// TestSnapshotOpensTheMonthAfterAsOf is the timing rule the whole layer
// hangs on: a balance read at the end of July is July's CLOSING figure, so
// it opens August. July must not see it.
func TestSnapshotOpensTheMonthAfterAsOf(t *testing.T) {
	a := newTestAccounts(t, testAccountsJSON)

	snap, ok := snapshotAt(t, a, yearMonth{2026, time.August})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.PrivateCents != 200000 {
		t.Errorf("cents = %d, want 200000", snap.PrivateCents)
	}
	if want := (yearMonth{2026, time.August}); snap.OpensMonth != want {
		t.Errorf("OpensMonth = %+v, want %+v (the month after the 31 July read)", snap.OpensMonth, want)
	}
}

// TestSnapshotOpensAcrossYearBoundary covers the December edge of that
// shift, where the opening month lands in the next calendar year.
func TestSnapshotOpensAcrossYearBoundary(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-12-31","balance":100}]}
	]}`)
	snap, ok := snapshotAt(t, a, yearMonth{2027, time.January})
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
	if _, ok := snapshotAt(t, a, yearMonth{2026, time.August}); ok {
		t.Error("no file should mean no snapshot")
	}
}

func TestAccountsNilIsSafe(t *testing.T) {
	var a *Accounts
	if _, err := a.File(context.Background()); err != nil {
		t.Errorf("nil Accounts should degrade quietly, got %v", err)
	}
	if _, ok := snapshotAt(t, a, yearMonth{2026, time.August}); ok {
		t.Error("nil Accounts should have no snapshot")
	}
	a.Evict() // must not panic
}

// TestTheReadingInForceIsTheNewestOneBeforeTheMonth is the selection rule the
// history hangs on. Three readings, and each month picks the one that was
// true when it started — a reading dated after the month cannot reach back
// into it, and an older one is not preferred just because it comes first in
// the file.
func TestTheReadingInForceIsTheNewestOneBeforeTheMonth(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"P","kind":"private","balances":[
			{"as_of":"2026-07-31","balance":2000},
			{"as_of":"2026-01-31","balance":500},
			{"as_of":"2026-04-30","balance":3000}
		]}
	]}`)
	tests := []struct {
		viewed    yearMonth
		wantCents int
		wantOpens yearMonth
	}{
		{yearMonth{2026, time.February}, 50000, yearMonth{2026, time.February}},
		{yearMonth{2026, time.April}, 50000, yearMonth{2026, time.February}},
		{yearMonth{2026, time.May}, 300000, yearMonth{2026, time.May}},
		{yearMonth{2026, time.July}, 300000, yearMonth{2026, time.May}},
		{yearMonth{2026, time.August}, 200000, yearMonth{2026, time.August}},
		{yearMonth{2027, time.March}, 200000, yearMonth{2026, time.August}},
	}
	for _, tt := range tests {
		t.Run(tt.viewed.String(), func(t *testing.T) {
			snap, ok := snapshotAt(t, a, tt.viewed)
			if !ok {
				t.Fatal("expected a snapshot")
			}
			if snap.PrivateCents != tt.wantCents {
				t.Errorf("cents = %d, want %d", snap.PrivateCents, tt.wantCents)
			}
			if snap.OpensMonth != tt.wantOpens {
				t.Errorf("OpensMonth = %+v, want %+v", snap.OpensMonth, tt.wantOpens)
			}
		})
	}
}

// TestAMonthBeforeEveryReadingHasNoSnapshot keeps the other half of that rule:
// history reaches backwards only as far as the earliest read. Before it there
// is still nothing to know, and nothing is guessed.
func TestAMonthBeforeEveryReadingHasNoSnapshot(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"P","kind":"private","balances":[{"as_of":"2026-04-30","balance":3000}]}
	]}`)
	if _, ok := snapshotAt(t, a, yearMonth{2026, time.April}); ok {
		t.Error("April closed on the read, so April itself must not see it")
	}
	if _, ok := snapshotAt(t, a, yearMonth{2026, time.May}); !ok {
		t.Error("May opens on the 30 April read and should see it")
	}
}

// TestALaterReadingReanchorsInsteadOfContinuingTheChain is the point of the
// whole feature. Two reads, 3 000 at the end of April and 2 000 at the end of
// July, against 1 000/month of expenses and no income:
//
//	May–July  carry the April read down, 3 000 → 2 000 → 1 000
//	August    ignores all of that and opens on the July read, 2 000
//
// Without re-anchoring August would open at 0. The second read is not extra
// evidence to reconcile against the projection; it replaces it.
func TestALaterReadingReanchorsInsteadOfContinuingTheChain(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","kind":"private","balances":[
			{"as_of":"2026-04-30","balance":3000},
			{"as_of":"2026-07-31","balance":2000}
		]}
	]}`)
	tests := []struct {
		month       time.Month
		wantOpening int
	}{
		{time.May, 300000},
		{time.June, 200000},
		{time.July, 100000},
		{time.August, 200000},
		{time.September, 100000},
	}
	for _, tt := range tests {
		t.Run(tt.month.String(), func(t *testing.T) {
			f := trk.ComputeMonth(context.Background(), 2026, tt.month)
			if f.AccountsErr != "" {
				t.Fatalf("AccountsErr = %q", f.AccountsErr)
			}
			if !f.ShowOpeningBalance {
				t.Fatal("expected an opening balance")
			}
			if f.OpeningBalanceCents != tt.wantOpening {
				t.Errorf("opening = %d, want %d", f.OpeningBalanceCents, tt.wantOpening)
			}
		})
	}
}

// TestAPotIsUnknownUntilItsFirstReading: an account read for the first time in
// July says nothing about May. Unknown is not zero — a company nobody has read
// yet must leave the whole company layer off, exactly as a file that declares
// no company account does.
func TestAPotIsUnknownUntilItsFirstReading(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-04-30","balance":3000}]},
		{"name":"Company","kind":"company","balances":[{"as_of":"2026-07-31","balance":5000}]}
	]}`)

	may, ok := snapshotAt(t, a, yearMonth{2026, time.May})
	if !ok {
		t.Fatal("the private read alone should still produce a snapshot")
	}
	if may.HasCompany {
		t.Error("the company was first read at the end of July — May cannot know it")
	}
	if may.CompanyCents != 0 || len(may.rowsOfKind(accountsdata.AccountKindCompany)) != 0 {
		t.Errorf("May produced a company figure: %d, %+v", may.CompanyCents, may.rowsOfKind(accountsdata.AccountKindCompany))
	}

	aug, _ := snapshotAt(t, a, yearMonth{2026, time.August})
	if !aug.HasCompany || aug.CompanyCents != 500000 {
		t.Errorf("August HasCompany = %v, cents = %d — want the July read", aug.HasCompany, aug.CompanyCents)
	}
}

// TestAnAccountReadTwiceInOneMonthIsRefused: two figures closing one month are
// two candidate openings for the next, with nothing to choose between them.
func TestAnAccountReadTwiceInOneMonthIsRefused(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"P","kind":"private","balances":[
			{"as_of":"2026-07-15","balance":100},
			{"as_of":"2026-07-31","balance":250}
		]}
	]}`)
	_, err := a.File(context.Background())
	if err == nil {
		t.Fatal("two readings in one month were accepted")
	}
	for _, want := range []string{"2026-07", "P"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestTwoAccountsWithOneNameAreRefused: the single-reading file summed them,
// which was already a trap. An account owns a history now, so the same name
// twice is two histories for one account and there is no rule for merging.
func TestTwoAccountsWithOneNameAreRefused(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Checking","kind":"private","balances":[{"as_of":"2026-06-30","balance":50}]},
		{"name":"Checking","kind":"private","balances":[{"as_of":"2026-09-30","balance":250}]}
	]}`)
	_, err := a.File(context.Background())
	if err == nil {
		t.Fatal("a duplicate account name was accepted")
	}
	if !strings.Contains(err.Error(), "Checking") {
		t.Errorf("error = %q, want it to name the account", err)
	}
}

// TestABrokenAccountsFileIsSaidOutLoud: a file the loader refuses used to
// switch the whole balance layer off in silence, which looks exactly like
// having no accounts.json at all. A hand-edited file that is wrong has to say
// so, or the figures quietly stop existing.
func TestABrokenAccountsFileIsSaidOutLoud(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Checking","kind":"private","balances":[{"as_of":"2026-06-30","balance":50}]},
		{"name":"Checking","kind":"private","balances":[{"as_of":"2026-07-31","balance":250}]}
	]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.AccountsErr == "" {
		t.Error("a rejected accounts.json produced no error on the page")
	}
	if f.ShowOpeningBalance {
		t.Error("a rejected accounts.json still rendered an opening balance")
	}
}

// TestSnapshotForAnchorsAtLatestAsOf covers the documented combining rule:
// accounts of one kind are summed and anchored at the newest as_of among
// them, so a single stale entry can't drag the effective date backwards.
func TestSnapshotForAnchorsAtLatestAsOf(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Old","kind":"private","balances":[{"as_of":"2026-03-31","balance":100}]},
		{"name":"New","kind":"private","balances":[{"as_of":"2026-09-30","balance":250}]}
	]}`)
	snap, ok := snapshotAt(t, a, yearMonth{2026, time.October})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.PrivateCents != 35000 {
		t.Errorf("cents = %d, want 35000 (both summed)", snap.PrivateCents)
	}
	if want := (yearMonth{2026, time.October}); snap.OpensMonth != want {
		t.Errorf("OpensMonth = %+v, want %+v (latest as_of wins, shifted a month)", snap.OpensMonth, want)
	}
}

// TestTheTwoKindsAreSummedApartButAnchoredTogether: each pot depletes by its
// own arithmetic, so the sums are separate — but there is one read date for
// the page, so a stale company entry drags the private opening month too, and
// the note that says so stays one note.
func TestTheTwoKindsAreSummedApartButAnchoredTogether(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-03-31","balance":100}]},
		{"name":"Company","kind":"company","balances":[{"as_of":"2026-09-30","balance":250}]}
	]}`)
	snap, ok := snapshotAt(t, a, yearMonth{2026, time.October})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.PrivateCents != 10000 {
		t.Errorf("PrivateCents = %d, want 10000 — the company balance is not private money", snap.PrivateCents)
	}
	if snap.CompanyCents != 25000 {
		t.Errorf("CompanyCents = %d, want 25000", snap.CompanyCents)
	}
	if !snap.HasCompany {
		t.Error("HasCompany is false with a company account declared, so the whole company layer would stay switched off")
	}
	if want := (yearMonth{2026, time.October}); snap.OpensMonth != want {
		t.Errorf("OpensMonth = %+v, want %+v — one anchor, the latest as_of among every account", snap.OpensMonth, want)
	}
	if got := snap.rowsOfKind(accountsdata.AccountKindPrivate); len(got) != 1 || got[0].Name != "Private" {
		t.Errorf("private rows = %+v, want just Private", got)
	}
	if got := snap.rowsOfKind(accountsdata.AccountKindCompany); len(got) != 1 || got[0].Name != "Company" {
		t.Errorf("company rows = %+v, want just Company", got)
	}
}

// TestAPrivateOnlyFileLeavesTheCompanyLayerOff is the compatibility invariant
// the whole change rests on: HasCompany is what switches the company stock on,
// so a file that declares none behaves exactly as it did before the pot
// existed rather than as a company holding zero.
func TestAPrivateOnlyFileLeavesTheCompanyLayerOff(t *testing.T) {
	snap, ok := snapshotAt(t, newTestAccounts(t, testAccountsJSON), yearMonth{2026, time.August})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.HasCompany {
		t.Error("HasCompany is true without a single company account")
	}
	if snap.CompanyCents != 0 {
		t.Errorf("CompanyCents = %d, want 0", snap.CompanyCents)
	}
	if len(snap.rowsOfKind(accountsdata.AccountKindCompany)) != 0 {
		t.Error("a private-only file produced company rows")
	}
}

// TestThePrivateOpeningListsOnlyPrivateAccounts: the rows under a figure are
// read as the figure's parts, so listing a company account beside a private
// opening that excludes it reads as arithmetic that does not add up.
func TestThePrivateOpeningListsOnlyPrivateAccounts(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]},
		{"name":"Company","kind":"company","balances":[{"as_of":"2026-07-31","balance":5000}]}
	]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.AccountsErr != "" {
		t.Fatalf("AccountsErr = %q", f.AccountsErr)
	}
	if f.OpeningBalanceCents != 200000 {
		t.Errorf("opening = %d, want 200000 — the company balance is not private money", f.OpeningBalanceCents)
	}
	if len(f.PrivateAccounts) != 1 || f.PrivateAccounts[0].Name != "Private" {
		t.Errorf("PrivateAccounts = %+v, want just the private account", f.PrivateAccounts)
	}
}

// TestAnAccountWithoutAKindIsRefused: which pot the money is in decides which
// side of the payroll cascade it lands on, so a default would move real money
// silently. budget.json's groups take the same line.
func TestAnAccountWithoutAKindIsRefused(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Nameless pot","balances":[{"as_of":"2026-07-31","balance":100}]}
	]}`)
	_, err := a.File(context.Background())
	if err == nil {
		t.Fatal("an account with no kind was accepted")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Errorf("error = %q, want it to name the missing field", err)
	}
}

// TestSnapshotForSkipsMalformedDate: a date nobody can parse drops that one
// reading. An account whose only reading is malformed drops out entirely; an
// account that also has a good one falls back to it rather than vanishing.
func TestSnapshotForSkipsMalformedDate(t *testing.T) {
	af := accountsdata.AccountsFile{Accounts: []accountsdata.Account{
		{Name: "Bad", Kind: accountsdata.AccountKindPrivate, Balances: []accountsdata.Reading{
			{AsOf: "not-a-date", Balance: 100},
		}},
		{Name: "Good", Kind: accountsdata.AccountKindPrivate, Balances: []accountsdata.Reading{
			{AsOf: "2026-05-31", Balance: 200},
			{AsOf: "also-not-a-date", Balance: 999},
		}},
	}}
	snap, ok := snapshotFor(af, yearMonth{2026, time.June})
	if !ok {
		t.Fatal("a malformed date should drop that reading, not the whole side")
	}
	if snap.PrivateCents != 20000 {
		t.Errorf("cents = %d, want 20000 (malformed entries excluded)", snap.PrivateCents)
	}
	if len(snap.AccountRow) != 1 || snap.AccountRow[0].Name != "Good" {
		t.Errorf("rows = %+v, want just the account with a readable date", snap.AccountRow)
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
		Personal: testLegislation(0.1892, 0.1378, 2112, 0.10),
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
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]}
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

// TestTheCompanyBalanceFundsPayroll: the balance is not a note beside the
// cascade, it is one of its inputs — a company holding money can afford a
// larger salary than its income alone would buy.
func TestTheCompanyBalanceFundsPayroll(t *testing.T) {
	p := bulgariaBands()
	r := p.rulesFor(yearMonth{2026, time.July})
	full := SalaryDecision{Mode: SalaryFull}

	broke := p.breakdown(1000, 0, 1, r, full, companyStock{Known: true})
	holding := p.breakdown(1000, 0, 1, r, full, companyStock{Known: true, OpeningCents: 300000})

	if holding.GrossSalaryCents <= broke.GrossSalaryCents {
		t.Errorf("gross = %d holding 3,000, %d holding nothing — the balance bought no salary",
			holding.GrossSalaryCents, broke.GrossSalaryCents)
	}
	// A full salary spends the balance down to nothing: that is what "full"
	// has always meant, and it is why the company had no stock to model until
	// the other modes came back.
	if holding.CompanyClosingCents != 0 {
		t.Errorf("closing = %d, want 0 — a full salary leaves nothing behind", holding.CompanyClosingCents)
	}
	// And an overdrawn company shrinks payroll instead of being ignored.
	overdrawn := p.breakdown(1000, 0, 1, r, full, companyStock{Known: true, OpeningCents: -50000})
	if overdrawn.GrossSalaryCents >= broke.GrossSalaryCents {
		t.Errorf("gross = %d while 500 overdrawn, %d level — the debt cost nothing",
			overdrawn.GrossSalaryCents, broke.GrossSalaryCents)
	}
}

// TestTheClosingBalanceIsTheRowsAboveIt: the page shows the opening, the
// income, the expenses, the employer contribution and the gross, then the
// closing figure. A reader adds those up, so the closing figure has to be
// exactly their sum rather than a separately-rounded near-miss.
func TestTheClosingBalanceIsTheRowsAboveIt(t *testing.T) {
	p := bulgariaBands()
	r := p.rulesFor(yearMonth{2026, time.July})

	for _, d := range []SalaryDecision{
		{Mode: SalaryMinimum},
		{Mode: SalaryNone},
		{Mode: SalaryFixed, FixedEUR: 1234.56},
		{Mode: SalaryFull},
	} {
		v := p.breakdown(3333.33, 111.11, 1, r, d, companyStock{Known: true, OpeningCents: 777777})
		want := v.CompanyOpeningCents + v.CompanyIncomeCents - v.CompanyExpensesCents -
			v.EmployerContribCents - v.GrossSalaryCents
		if v.CompanyClosingCents != want {
			t.Errorf("%s: closing = %d, want %d — the rows on the page do not add up to it",
				d.Mode, v.CompanyClosingCents, want)
		}
	}
}

// TestAnUnknownCompanyBalanceChangesNothing is the compatibility fence: a
// config with no company account must produce the figures it produced before
// the pot existed, so "unknown" cannot be modelled as "holds zero".
func TestAnUnknownCompanyBalanceChangesNothing(t *testing.T) {
	p := bulgariaBands()
	r := p.rulesFor(yearMonth{2026, time.July})
	d := SalaryDecision{Mode: SalaryFull}

	unknown := p.breakdown(4000, 250, 1, r, d, companyStock{})
	zero := p.breakdown(4000, 250, 1, r, d, companyStock{Known: true})

	if unknown.GrossSalaryCents != zero.GrossSalaryCents {
		t.Errorf("gross = %d unknown, %d known-zero — they must agree on the money",
			unknown.GrossSalaryCents, zero.GrossSalaryCents)
	}
	// They differ only in whether the page has a balance to show.
	if unknown.ShowCompanyBalance {
		t.Error("an unknown balance produced a company balance row")
	}
	if !zero.ShowCompanyBalance {
		t.Error("a known balance of zero produced no company balance row — zero is a figure")
	}
}

// TestTheCompanyBalanceRollsForwardAndCompounds is the company sibling of the
// private roll-forward: a month at the statutory minimum leaves the rest
// behind, and the next month opens on it rather than on the read figure.
func TestTheCompanyBalanceRollsForwardAndCompounds(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]},
		{"name":"Company","kind":"company","balances":[{"as_of":"2026-07-31","balance":5000}]}
	]}`)
	trk.Personal = bulgariaBands()
	plan, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-01", Mode: "minimum"}})
	if err != nil {
		t.Fatal(err)
	}
	trk.Personal.Salary = plan

	// The minimum wage and its contribution are the only company outflow, so
	// each month closes a fixed amount below where it opened.
	aug := trk.ComputeMonth(context.Background(), 2026, time.August)
	if aug.AccountsErr != "" {
		t.Fatalf("AccountsErr = %q", aug.AccountsErr)
	}
	if !aug.FundingPersonal.ShowCompanyBalance {
		t.Fatal("no company balance in the month the snapshot opens")
	}
	if aug.FundingPersonal.CompanyOpeningCents != 500000 {
		t.Errorf("August opening = %d, want the 5,000 read at the end of July", aug.FundingPersonal.CompanyOpeningCents)
	}
	augClose := aug.FundingPersonal.CompanyClosingCents
	if augClose >= 500000 {
		t.Fatalf("August closed at %d without paying a salary out of 5,000", augClose)
	}

	sep := trk.ComputeMonth(context.Background(), 2026, time.September)
	if sep.FundingPersonal.CompanyOpeningCents != augClose {
		t.Errorf("September opened at %d, want August's close of %d — the balance reset instead of carrying",
			sep.FundingPersonal.CompanyOpeningCents, augClose)
	}

	// And months before the read date do not see it at all.
	jul := trk.ComputeMonth(context.Background(), 2026, time.July)
	if jul.FundingPersonal.ShowCompanyBalance {
		t.Error("July saw a balance that was only read at the end of July")
	}
}

// TestTheCompanyBalanceIsVisibleOnBothSidesOfTheCascade: the balance goes in
// before payroll and what survives comes out after it, so the page has to show
// both ends — one figure without the other is unreadable.
func TestTheCompanyBalanceIsVisibleOnBothSidesOfTheCascade(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private Checking","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]},
		{"name":"Company Checking","kind":"company","balances":[{"as_of":"2026-07-31","balance":5000}]}
	]}`)
	trk.Personal = bulgariaBands()
	plan, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-01", Mode: "minimum"}})
	if err != nil {
		t.Fatal(err)
	}
	trk.Personal.Salary = plan

	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	for _, want := range []string{
		`class="label">In the company`,
		`class="label">Left in the company`,
		"Company Checking",
		// The private figure has to say which pot it is now that there are two.
		`class="label">Private opening balance`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page never says %q", want)
		}
	}
	// The company opening is read before payroll, so it belongs above the
	// gross salary it paid for, and the closing below it.
	opening := strings.Index(body, `class="label">In the company`)
	gross := strings.Index(body, `class="label">Gross salary`)
	closing := strings.Index(body, `class="label">Left in the company`)
	if !(opening < gross && gross < closing) {
		t.Errorf("rows are out of order: opening at %d, gross at %d, closing at %d", opening, gross, closing)
	}
}

// TestAvailableIsOpeningPlusNetIncome pins the middle line of the budget
// ledger and its relationship to the two around it: Available is what there
// is before private expenses come off, and Balance is what's left after.
func TestAvailableIsOpeningPlusNetIncome(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]}
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
		{"name":"Private","kind":"private","balances":[{"as_of":"2026-07-31","balance":2000}]}
	]}`)

	positive := trk.ComputeMonth(context.Background(), 2026, time.August)
	if positive.OpeningBalanceCents <= 0 {
		t.Fatalf("August opening = %d, want positive for the control case", positive.OpeningBalanceCents)
	}
	recPos := httptest.NewRecorder()
	RenderPage(recPos, positive)
	if strings.Contains(recPos.Body.String(), `class="row net neg"><span class="label">Private opening balance`) {
		t.Error("a positive opening balance must not render as negative")
	}

	negative := trk.ComputeMonth(context.Background(), 2026, time.November)
	if negative.OpeningBalanceCents >= 0 {
		t.Fatalf("November opening = %d, want negative for this test to mean anything", negative.OpeningBalanceCents)
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, negative)
	body := rec.Body.String()
	if !strings.Contains(body, `class="row net neg"><span class="label">Private opening balance`) {
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

// TestVeryStaleBalanceStillComputes is the point of the whole layer: going
// a long time without reading your bank makes the figure *nag*, never makes
// it disappear. A balance last read years ago is still carried forward and
// still shown.
func TestVeryStaleBalanceStillComputes(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Neglected","kind":"private","balances":[{"as_of":"2024-01-31","balance":2000}]}
	]}`)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)

	if f.AccountsErr != "" {
		t.Fatalf("AccountsErr = %q — a stale balance must still be carried, not withheld", f.AccountsErr)
	}
	if !f.ShowOpeningBalance {
		t.Error("expected the opening balance to still render for a long-stale snapshot")
	}
}

// TestTheDashboardAccountRowsAreNameAndFigureOnly pins what the ledger
// deliberately does not say. An account row is a part of the figure above it,
// and every extra clause — when it was read, what the note in accounts.json
// says, why the company is overdrawn, how long since the bank was checked —
// was prose on a page whose job is arithmetic. The read date moved to the
// spending page, which is where it is acted on; the rest is in the file.
func TestTheDashboardAccountRowsAreNameAndFigureOnly(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Revolut Private","kind":"private","balances":[
			{"as_of":"2026-07-31","balance":2000,"note":"Not yet funded — placeholder until the main part lands"}
		]},
		{"name":"Revolut Business","kind":"company","balances":[
			{"as_of":"2026-07-31","balance":-10,"note":"Overdrawn by the June Revolut Business Fee"}
		]}
	]}`)
	trk.Personal = bulgariaBands()
	fixed := 2500.0
	plan, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-01", Mode: "fixed", Amount: &fixed}})
	if err != nil {
		t.Fatal(err)
	}
	trk.Personal.Salary = plan

	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	// The fixed salary overdraws the company, which is what used to earn a
	// paragraph of explanation underneath it.
	if f.FundingPersonal.CompanyClosingCents >= 0 {
		t.Fatal("the company is not overdrawn, so the note this guards would not have fired anyway")
	}
	for _, unwanted := range []string{
		"as of ",
		"as read end of",
		"carried from",
		"Not yet funded",
		"Overdrawn by the June",
		"overdrawn — it paid out",
		"go check your bank",
		`class="basis"`,
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the dashboard still says %q", unwanted)
		}
	}
	// The figures themselves stay, with their names.
	for _, want := range []string{"Revolut Private", "Revolut Business", "Private opening balance", "In the company"} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard lost %q", want)
		}
	}
}

// TestImplausibleAsOfDateErrors covers the one case that still refuses: a
// mistyped year, which would otherwise fan out into an external API call
// per calendar year spanned.
func TestImplausibleAsOfDateErrors(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Typo","kind":"private","balances":[{"as_of":"1999-01-31","balance":2000}]}
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

// TestAPrivateOnlyPageShowsNoCompanyRows is the page-level half of the
// compatibility guard: someone who never declares a company account must not
// find new rows on their ledger, however much machinery sits behind them.
func TestAPrivateOnlyPageShowsNoCompanyRows(t *testing.T) {
	trk := accountsTracker(t, testAccountsJSON)
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.FundingPersonal.ShowCompanyBalance {
		t.Error("a private-only file switched the company balance on")
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()
	for _, unwanted := range []string{"In the company", "Left in the company", "target "} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the page says %q without a company account", unwanted)
		}
	}
	// The private figure keeps its name either way, so the label is not a
	// surprise the first time a company account appears.
	if !strings.Contains(body, "Private opening balance") {
		t.Error("the private opening balance lost its label")
	}
}

func TestSnapshotForNegativeBalance(t *testing.T) {
	a := newTestAccounts(t, `{"accounts":[
		{"name":"Overdrawn","kind":"private","balances":[{"as_of":"2026-05-31","balance":-150.5}]}
	]}`)
	snap, ok := snapshotAt(t, a, yearMonth{2026, time.June})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.PrivateCents != -15050 {
		t.Errorf("cents = %d, want -15050", snap.PrivateCents)
	}
}

// TestAPublishedReadingIsInForceBeforeItDeploys: a balance recorded through the
// API is a commit, and the image built from it is minutes away. Until then the
// bytes just committed have to answer every read, or the agent records a
// balance and is told the account still holds the old one.
func TestAPublishedReadingIsInForceBeforeItDeploys(t *testing.T) {
	a := newTestAccounts(t, testAccountsJSON)
	if snap, _ := snapshotAt(t, a, yearMonth{2026, time.September}); snap.PrivateCents != 200000 {
		t.Fatalf("cents = %d before the write, want the 31 July reading", snap.PrivateCents)
	}

	a.Publish([]byte(`{"accounts":[
		{"name":"Private Checking","kind":"private","balances":[
			{"as_of":"2026-07-31","balance":2000},
			{"as_of":"2026-08-31","balance":1750}
		]}
	]}`))

	snap, ok := snapshotAt(t, a, yearMonth{2026, time.September})
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.PrivateCents != 175000 {
		t.Errorf("cents = %d, want 175000 — September opens on the reading just written", snap.PrivateCents)
	}
	if want := (yearMonth{2026, time.September}); snap.OpensMonth != want {
		t.Errorf("OpensMonth = %+v, want %+v", snap.OpensMonth, want)
	}
}

// TestThePublishedFileStepsAsideWhenTheDeployLands: the overlay is dropped as
// soon as the file says the same thing, so nothing lingers in memory claiming
// an authority it no longer has.
func TestThePublishedFileStepsAsideWhenTheDeployLands(t *testing.T) {
	const deployed = `{"accounts":[
		{"name":"Private Checking","kind":"private","balances":[{"as_of":"2026-08-31","balance":1750}]}
	]}`
	a := newTestAccounts(t, testAccountsJSON)
	a.Publish([]byte(deployed))

	a.FS = fstest.MapFS{accountsPath: &fstest.MapFile{Data: []byte(deployed)}}
	a.Evict()
	if snap, _ := snapshotAt(t, a, yearMonth{2026, time.September}); snap.PrivateCents != 175000 {
		t.Fatalf("cents = %d after the deploy, want 175000", snap.PrivateCents)
	}

	// The deploy has replaced the file, so the overlay is gone and the file
	// alone decides — including when it moves on again.
	a.FS = fstest.MapFS{accountsPath: &fstest.MapFile{Data: []byte(testAccountsJSON)}}
	a.Evict()
	if snap, _ := snapshotAt(t, a, yearMonth{2026, time.September}); snap.PrivateCents != 200000 {
		t.Errorf("cents = %d, want 200000 — a forgotten overlay is still answering", snap.PrivateCents)
	}
}

// TestAPublishedFileIsStillValidated: the overlay carries bytes through the
// same parse and validation a file gets, so it can never surface a document
// the loader would have refused from disk.
func TestAPublishedFileIsStillValidated(t *testing.T) {
	bad := []byte(`{"accounts":[
		{"name":"P","kind":"private","balances":[
			{"as_of":"2026-08-05","balance":10},
			{"as_of":"2026-08-31","balance":20}
		]}
	]}`)

	fromOverlay := &Accounts{FS: fstest.MapFS{}}
	fromOverlay.Publish(bad)
	_, overlayErr := fromOverlay.File(context.Background())

	fromDisk := &Accounts{FS: fstest.MapFS{accountsPath: &fstest.MapFile{Data: bad}}}
	_, diskErr := fromDisk.File(context.Background())

	if overlayErr == nil {
		t.Fatal("two readings in one month were accepted from the overlay")
	}
	if diskErr == nil {
		t.Fatal("the same file was accepted from disk — this test proves nothing")
	}
	if overlayErr.Error() != diskErr.Error() {
		t.Errorf("overlay refused with %q, disk with %q — the overlay must be judged by the same rules", overlayErr, diskErr)
	}
}
