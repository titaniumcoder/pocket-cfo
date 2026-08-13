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
    { "name": "Private Checking", "kind": "private", "balance": 2000, "as_of": "2026-07-31" }
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
		{"name":"Private","kind": "private", "balance":100,"as_of":"2026-12-31"}
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
		{"name":"Old","kind": "private", "balance":100,"as_of":"2026-03-31"},
		{"name":"New","kind": "private", "balance":250,"as_of":"2026-09-30"}
	]}`)
	snap, ok := a.Snapshot(context.Background())
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
		{"name":"Private","kind":"private","balance":100,"as_of":"2026-03-31"},
		{"name":"Company","kind":"company","balance":250,"as_of":"2026-09-30"}
	]}`)
	snap, ok := a.Snapshot(context.Background())
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
	snap, ok := newTestAccounts(t, testAccountsJSON).Snapshot(context.Background())
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
		{"name":"Private","kind":"private","balance":2000,"as_of":"2026-07-31"},
		{"name":"Company","kind":"company","balance":5000,"as_of":"2026-07-31"}
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
		{"name":"Nameless pot","balance":100,"as_of":"2026-07-31"}
	]}`)
	_, err := a.File(context.Background())
	if err == nil {
		t.Fatal("an account with no kind was accepted")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Errorf("error = %q, want it to name the missing field", err)
	}
}

func TestSnapshotForSkipsMalformedDate(t *testing.T) {
	af := accountsdata.AccountsFile{Accounts: []accountsdata.Account{
		{Name: "Bad", Kind: accountsdata.AccountKindPrivate, Balance: 100, AsOf: "not-a-date"},
		{Name: "Good", Kind: accountsdata.AccountKindPrivate, Balance: 200, AsOf: "2026-05-31"},
	}}
	snap, ok := snapshotFor(af)
	if !ok {
		t.Fatal("a malformed date should drop that account, not the whole side")
	}
	if snap.PrivateCents != 20000 {
		t.Errorf("cents = %d, want 20000 (malformed entry excluded)", snap.PrivateCents)
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
		{"name":"Private","kind": "private", "balance":2000,"as_of":"2026-07-31"}
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
		{"name":"Private","kind":"private","balance":2000,"as_of":"2026-07-31"},
		{"name":"Company","kind":"company","balance":5000,"as_of":"2026-07-31"}
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
		{"name":"Private Checking","kind":"private","balance":2000,"as_of":"2026-07-31"},
		{"name":"Company Checking","kind":"company","balance":5000,"as_of":"2026-07-31"}
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
		{"name":"Private","kind": "private", "balance":2000,"as_of":"2026-07-31"}
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
		{"name":"Private","kind": "private", "balance":2000,"as_of":"2026-07-31"}
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
	trkFresh := accountsTracker(t, `{"accounts":[{"name":"P","kind": "private", "balance":2000,"as_of":"`+fresh.Format("2006-01-02")+`"}]}`)
	f := trkFresh.ComputeMonth(context.Background(), viewed.Year, viewed.Month)
	if f.AccountsStaleNote != "" {
		t.Errorf("a recently read balance should not nag, got %q", f.AccountsStaleNote)
	}

	old := now.AddDate(0, 0, -100)
	trkOld := accountsTracker(t, `{"accounts":[{"name":"P","kind": "private", "balance":2000,"as_of":"`+old.Format("2006-01-02")+`"}]}`)
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
		{"name":"Neglected","kind": "private", "balance":2000,"as_of":"2024-01-31"}
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
		{"name":"Typo","kind": "private", "balance":2000,"as_of":"1999-01-31"}
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
		{"name":"Overdrawn","kind": "private", "balance":-150.5,"as_of":"2026-05-31"}
	]}`)
	snap, ok := a.Snapshot(context.Background())
	if !ok {
		t.Fatal("expected a snapshot")
	}
	if snap.PrivateCents != -15050 {
		t.Errorf("cents = %d, want -15050", snap.PrivateCents)
	}
}
