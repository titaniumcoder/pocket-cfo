package tracker

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// loanTracker is a tracker anchored on one company account, one private
// account and a stated director's loan, with the months' statements supplied.
func loanTracker(t *testing.T, loanAsOf string, loanBalance float64, actuals map[string]string) *Tracker {
	t.Helper()
	trk := actualsTracker(t, actuals)
	trk.Accounts = newTestAccounts(t, fmt.Sprintf(`{"accounts":[
		{"name":"Private Checking","kind":"private","balances":[{"as_of":"2026-07-31","balance":4200}]},
		{"name":"Company Checking","kind":"company","balances":[{"as_of":"2026-07-31","balance":6800}]}
	],"director_loan":{"balances":[{"as_of":%q,"balance":%v}]}}`, loanAsOf, loanBalance))
	return trk
}

// TestTheFourRowsAddDownToTheDirectorLoanAtTheEnd: each row carries the sign
// of its own effect, so a reader can add the column and arrive at the figure
// that closes it — which §10 calls the first thing a reader checks.
func TestTheFourRowsAddDownToTheDirectorLoanAtTheEnd(t *testing.T) {
	trk := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[
				{"id":"s1","date":"2026-08-05","description":"To Rico","amount":2400,"account":"Company Checking","ignored":"salary paid across","movement":"salary_transfer"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)

	if !f.ShowDirectorLoan {
		t.Fatal("the director's loan is not shown for a month with a stated opening figure")
	}
	if f.LoanOpeningCents != 1240000 {
		t.Errorf("opening = %d, want the stated 12400", f.LoanOpeningCents)
	}
	if f.LoanMovementCents != -240000 {
		t.Errorf("movement = %d, want the 2400 that crossed, as a settlement", f.LoanMovementCents)
	}
	if got := f.LoanOpeningCents + f.LoanNetIncomeCents + f.LoanMovementCents; got != f.LoanClosingCents {
		t.Errorf("the rows add to %d and the closing figure says %d", got, f.LoanClosingCents)
	}
}

// TestATransferReducesTheDirectorLoanInTheMonthItLandsIn, and money paid in
// raises it: the sign on the statement line carries the direction, so neither
// needs a branch of its own.
func TestMoneyCrossingMovesTheLoanBothWays(t *testing.T) {
	settle := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)

	putIn := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"c1","date":"2026-08-06","description":"From Rico","amount":-500,"account":"Company Checking","ignored":"money paid into the company","movement":"owner_contribution"}]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)

	if settle.LoanMovementCents != -500000 {
		t.Errorf("a draw moved the loan by %d, want it settling 5000 of what is owed", settle.LoanMovementCents)
	}
	if putIn.LoanMovementCents != 50000 {
		t.Errorf("money paid in moved the loan by %d, want it raising what is owed by 500", putIn.LoanMovementCents)
	}
}

// TestATaxPaymentLeavesTheDirectorLoanAlone: the two tax movements leave the
// company but never reach the owner, so they settle nothing.
func TestATaxPaymentLeavesTheDirectorLoanAlone(t *testing.T) {
	f := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"tx1","date":"2026-08-06","description":"NRA","amount":1000,"account":"Company Checking","ignored":"corporate tax","movement":"corporate_tax"}]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)

	if f.LoanMovementCents != 0 {
		t.Errorf("a tax payment moved the loan by %d — it left the company but never reached the owner", f.LoanMovementCents)
	}
}

// TestTheDirectorLoanIsNotAFigureOfZeroWhenUnknown: a month before every
// reading says so rather than showing nothing, or a zero.
func TestTheDirectorLoanIsNotAFigureOfZeroWhenUnknown(t *testing.T) {
	f := loanTracker(t, "2026-12-31", 12400, nil).ComputeMonth(context.Background(), 2026, time.August)

	if f.DirectorLoanUnknown == "" {
		t.Fatal("a month before every reading says nothing about the loan being unknown")
	}
	if f.LoanClosingCents != 0 || f.LoanOpeningCents != 0 {
		t.Errorf("an unknown loan carries figures: opening %d closing %d", f.LoanOpeningCents, f.LoanClosingCents)
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()
	if !strings.Contains(body, "is not known") {
		t.Error("the page does not say the loan is not known")
	}
}

// TestADrawMovesTheActualPrivateFigureAndLeavesThePlanAlone is where the two
// columns earn their keep. A draw is money the owner now has, so the Actual
// column rises by it; a draw is in no plan, so the planned column may not know
// it happened at all. Marking the line is what tells the page which of the two
// it is, and it must move exactly one of them.
func TestADrawMovesTheActualPrivateFigureAndLeavesThePlanAlone(t *testing.T) {
	const withDraw = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`
	const same = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw"}]}`

	marked := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": withDraw}).
		ComputeMonth(context.Background(), 2026, time.August)
	unmarked := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": same}).
		ComputeMonth(context.Background(), 2026, time.August)

	for _, same := range []struct {
		name       string
		got, other int
	}{
		{"Balance", marked.BalanceCents, unmarked.BalanceCents},
		{"Available to spend", marked.AvailableCents, unmarked.AvailableCents},
		{"private opening", marked.OpeningBalanceCents, unmarked.OpeningBalanceCents},
		{"company closing", marked.FundingPersonal.CompanyClosingCents, unmarked.FundingPersonal.CompanyClosingCents},
	} {
		if same.got != same.other {
			t.Errorf("planned %s moved from %d to %d because a line was marked", same.name, same.other, same.got)
		}
	}
	for _, moved := range []struct {
		name       string
		got, other int
	}{
		{"Available to spend", marked.ActualAvailableCents, unmarked.ActualAvailableCents},
		{"Balance", marked.ActualBalanceCents, unmarked.ActualBalanceCents},
	} {
		if moved.got-moved.other != 500000 {
			t.Errorf("actual %s moved by %d when the owner drew 5000", moved.name, moved.got-moved.other)
		}
	}
	if marked.ArrivedPrivatelyCents != 500000 {
		t.Errorf("the draw reached the owner by %d, want 5000", marked.ArrivedPrivatelyCents)
	}
	if marked.LoanMovementCents == unmarked.LoanMovementCents {
		t.Error("marking the line changed nothing at all, so this test proves nothing")
	}
}

// TestADrawIsBothAnAssetAndALiabilityAndTheTwoAgree: the owner is 5000 richer
// and owes the company 5000 more, which is one transaction with two sides and
// not two competing accounts of it. Booking only the liability, which this page
// used to do, is what made Available to spend read low by every draw taken.
func TestADrawIsBothAnAssetAndALiabilityAndTheTwoAgree(t *testing.T) {
	f := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)

	if f.ArrivedPrivatelyCents != 500000 {
		t.Fatalf("the asset side is %d, want the 5000 drawn", f.ArrivedPrivatelyCents)
	}
	if f.LoanMovementCents != -500000 {
		t.Fatalf("the liability side is %d, want the 5000 drawn", f.LoanMovementCents)
	}
	if f.ArrivedPrivatelyCents != -f.LoanMovementCents {
		t.Errorf("the two sides of one draw disagree: %d reached the owner, %d settled the loan",
			f.ArrivedPrivatelyCents, f.LoanMovementCents)
	}
}

// TestTheTwoColumnsAgreeExactlyWhenTheMonthWentToPlan is the invariant that
// holds the exclusion set honest. The planned column assumes net income
// arrived; the actual one assumes only the net salary and counts everything
// else from the statements. So a month where the company transferred precisely
// the net salary the plan sized, and nothing else crossed, must read the same
// figure twice — and a draw on top must move exactly one of them.
//
// Nothing in the type system ties CrossedOutsidePayroll to the internals of
// NetIncomeCents. If somebody adds a component to net income — a bonus, a
// reimbursement, a second kind of distribution — without giving it a movement
// of its own, the two columns part company here and this test says so.
func TestTheTwoColumnsAgreeExactlyWhenTheMonthWentToPlan(t *testing.T) {
	// What the plan says will arrive, read off the page rather than restated.
	planned := loanTracker(t, "2026-07-31", 12400, nil).ComputeMonth(context.Background(), 2026, time.August)
	netSalary := planned.FundingPersonal.NetSalaryCents()
	if netSalary == 0 {
		t.Fatal("the fixture pays no salary, so this test would hold for the wrong reason")
	}

	month := func(extra string) Figures {
		return loanTracker(t, "2026-07-31", 12400, map[string]string{
			"actuals/2026-08.json": fmt.Sprintf(`{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
				"transactions":[
					{"id":"s1","date":"2026-08-05","description":"To Rico","amount":%.2f,"account":"Company Checking","ignored":"salary paid across","movement":"salary_transfer"}%s]}`,
				float64(netSalary)/100, extra),
		}).ComputeMonth(context.Background(), 2026, time.August)
	}

	toPlan := month("")
	if toPlan.ActualAvailableCents != toPlan.AvailableCents {
		t.Errorf("the month went exactly to plan and the columns still read %d planned against %d actual — the exclusion set has drifted from net income",
			toPlan.AvailableCents, toPlan.ActualAvailableCents)
	}

	andADraw := month(`,
					{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}`)
	if got := andADraw.ActualAvailableCents - andADraw.AvailableCents; got != 500000 {
		t.Errorf("with the salary paid to plan and 5000 drawn on top, the columns differ by %d, want the draw alone", got)
	}
}

// TestADistributionReachesThePrivateSideOnlyWhenItIsPaidAndASalaryAlways:
// §10 says a salary is settled every month by a transfer while a distribution
// is settled irregularly or never in cash, which is the whole reason a loan
// exists for one and not the other. So the private side may assume the salary
// arrived and must be shown the distribution. Counting the salary transfer too
// would credit it twice.
func TestADistributionReachesThePrivateSideOnlyWhenItIsPaidAndASalaryAlways(t *testing.T) {
	const coverage = `"coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}]`

	quiet := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08",` + coverage + `,"transactions":[]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)

	salary := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08",` + coverage + `,
			"transactions":[{"id":"s1","date":"2026-08-05","description":"To Rico","amount":2400,"account":"Company Checking","ignored":"salary paid across","movement":"salary_transfer"}]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)

	paidOut := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08",` + coverage + `,
			"transactions":[{"id":"p1","date":"2026-08-05","description":"To Rico","amount":900,"account":"Company Checking","ignored":"dividend paid out","movement":"dividend_payout"}]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)

	if salary.ActualAvailableCents != quiet.ActualAvailableCents {
		t.Errorf("marking the salary transfer moved Available to spend from %d to %d — the plan already assumed it arrived",
			quiet.ActualAvailableCents, salary.ActualAvailableCents)
	}
	if got := paidOut.ActualAvailableCents - quiet.ActualAvailableCents; got != 90000 {
		t.Errorf("a distribution actually paid moved Available to spend by %d, want the 900 that crossed", got)
	}
}

// TestADirectorLoanTheOwnerOwesTheCompanyCarriesItsSignAndReadsRed: the label
// is neutral in both directions and the colour carries which way round it is.
func TestADirectorLoanTheOwnerOwesTheCompanyCarriesItsSignAndReadsRed(t *testing.T) {
	owed := Figures{Currency: "€", ShowDirectorLoan: true, LoanOpeningCents: 1240000, LoanClosingCents: 950000}
	owing := Figures{Currency: "€", ShowDirectorLoan: true, LoanOpeningCents: -1240000, LoanClosingCents: -950000}

	rec := httptest.NewRecorder()
	RenderPage(rec, owed)
	if strings.Contains(rec.Body.String(), `class="row net neg"><span class="label">At the end`) {
		t.Error("a loan the company owes the owner reads red")
	}
	if !strings.Contains(rec.Body.String(), `class="row net goodamt"><span class="label">At the end`) {
		t.Error("a loan the company owes the owner does not read green")
	}

	rec = httptest.NewRecorder()
	RenderPage(rec, owing)
	body := rec.Body.String()
	if !strings.Contains(body, `class="row net neg"><span class="label">At the end`) {
		t.Error("a loan the owner owes the company does not read red")
	}
	if !strings.Contains(body, "-9,500") {
		t.Error("the figure lost its sign, so the direction is only in the colour")
	}
	// A direction is not a warning, so it gets no mark.
	if strings.Contains(body, "flagged") {
		t.Error("the loan is marked as a flag rather than coloured as a direction")
	}
}

// TestAPayoutReadsRedAndMoneyPutInReadsGreen: the movement row is the one that
// genuinely goes either way, and it is reality that decides.
func TestAPayoutReadsRedAndMoneyPutInReadsGreen(t *testing.T) {
	paidOut := Figures{Currency: "€", ShowDirectorLoan: true, LoanMovementCents: -240000}
	putIn := Figures{Currency: "€", ShowDirectorLoan: true, LoanMovementCents: 50000}

	rec := httptest.NewRecorder()
	RenderPage(rec, paidOut)
	if !strings.Contains(rec.Body.String(), `class="row neg"><span class="label">Money movement`) {
		t.Error("money paid out to the owner does not read red")
	}

	rec = httptest.NewRecorder()
	RenderPage(rec, putIn)
	body := rec.Body.String()
	if !strings.Contains(body, `class="row goodamt"><span class="label">Money movement`) {
		t.Error("money the owner put in does not read green")
	}
	if !strings.Contains(body, "+500") {
		t.Error("the movement row lost its sign, so the four figures no longer add down")
	}
}

// TestTheDirectorLoanRendersAfterTheBalanceRow: it is the figure you look at
// once you know where the month stands, not before.
func TestTheDirectorLoanRendersAfterTheBalanceRow(t *testing.T) {
	f := Figures{Currency: "€", ShowBalance: true, BalanceCents: 100000,
		ShowDirectorLoan: true, LoanOpeningCents: 1240000, LoanClosingCents: 950000}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	balance := strings.Index(body, `class="label">Balance`)
	loan := strings.Index(body, "Director&rsquo;s loan")
	if balance < 0 || loan < 0 {
		t.Fatalf("balance at %d, loan at %d — one of them is missing", balance, loan)
	}
	if loan < balance {
		t.Error("the director's loan is rendered before the Balance row")
	}
}

// TestTheYearViewShowsNoDirectorLoanBecauseItReadsNoBalances: a year spreads
// company expenses evenly across its range, which smooths a flow but reorders
// a balance — so a twelve-month loan would be one the twelve month views never
// agree with.
func TestTheYearViewShowsNoDirectorLoanBecauseItReadsNoBalances(t *testing.T) {
	f := loanTracker(t, "2026-07-31", 12400, nil).ComputeYear(context.Background(), 2026)
	if f.ShowDirectorLoan {
		t.Error("the year view shows a director's loan")
	}
}

// TestTwoLinesMarkedAsTheSameMovementOnOneDaySaySoWithoutFailingTheMonth: the
// sign rule cannot catch a transfer marked twice on the company side. Refusing
// it would take the whole month off the dashboard for a heuristic with real
// false positives, so it is a note.
func TestTwoLinesMarkedAsTheSameMovementOnOneDaySaySoWithoutFailingTheMonth(t *testing.T) {
	trk := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[
				{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"},
				{"id":"d2","date":"2026-08-06","description":"To Rico again","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)

	if !f.ShowDirectorLoan {
		t.Fatal("the month was taken off the dashboard for a heuristic")
	}
	found := false
	for _, note := range f.DirectorLoanNotes {
		if strings.Contains(note, "counted twice") {
			found = true
		}
	}
	if !found {
		t.Errorf("two lines marked identically on one day and no note: %v", f.DirectorLoanNotes)
	}
	// It is a note, not a refusal: the figure is still shown, doubled.
	if f.LoanMovementCents != -1000000 {
		t.Errorf("movement = %d, want both lines counted so the note has something to warn about", f.LoanMovementCents)
	}
}

// TestAnOwnerDrawLeavesTheCompanyInTheFigureTheBankSaw: the cash went, so the
// figure that claims to say what the bank holds has to say it went. Before
// this, a marked draw was fully imported, listed on the spending page, and
// still left both company figures reading too high by exactly its amount.
func TestAnOwnerDrawLeavesTheCompanyInTheFigureTheBankSaw(t *testing.T) {
	const drawn = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`
	const unmarked = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw"}]}`

	marked := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": drawn}).
		ComputeMonth(context.Background(), 2026, time.August)
	plain := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": unmarked}).
		ComputeMonth(context.Background(), 2026, time.August)

	if got := plain.ActualCompanyClosingCents - marked.ActualCompanyClosingCents; got != 500000 {
		t.Errorf("the bank figure fell by %d when the draw was marked, want the 5000 that left", got)
	}
	// The planned column is a plan: a draw is in no plan, so it does not move.
	if marked.FundingPersonal.CompanyClosingCents != plain.FundingPersonal.CompanyClosingCents {
		t.Error("marking a draw moved the planned closing figure, which knows nothing of draws")
	}
	// And the other half of the same movement: what the company lost, the owner
	// got. Both figures claim to say what really happened, so they have to agree
	// on the amount and disagree only on the direction.
	if got := marked.ActualAvailableCents - plain.ActualAvailableCents; got != 500000 {
		t.Errorf("the owner's side rose by %d while the company's fell by 5000", got)
	}
	// The planned private figures know nothing of draws either.
	if marked.BalanceCents != plain.BalanceCents || marked.AvailableCents != plain.AvailableCents {
		t.Error("marking a draw moved a planned private figure")
	}
}

// TestASalaryTransferDoesNotTakeTheSalaryOutTwice: the cascade already
// subtracted the gross, so the transfer that pays it must not subtract it
// again — the one movement deliberately outside the bank figure.
func TestASalaryTransferDoesNotTakeTheSalaryOutTwice(t *testing.T) {
	const paid = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"s1","date":"2026-08-05","description":"To Rico","amount":2400,"account":"Company Checking","ignored":"salary paid across","movement":"salary_transfer"}]}`
	const unmarked = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"s1","date":"2026-08-05","description":"To Rico","amount":2400,"account":"Company Checking","ignored":"salary paid across"}]}`

	marked := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": paid}).
		ComputeMonth(context.Background(), 2026, time.August)
	plain := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": unmarked}).
		ComputeMonth(context.Background(), 2026, time.August)

	if marked.ActualCompanyClosingCents != plain.ActualCompanyClosingCents {
		t.Errorf("the bank figure moved by %d when a salary transfer was marked — the gross already covers it",
			plain.ActualCompanyClosingCents-marked.ActualCompanyClosingCents)
	}
	// It still settles the loan, which is the whole reason it is marked.
	if marked.LoanMovementCents == plain.LoanMovementCents {
		t.Error("marking the salary transfer settled nothing on the loan")
	}
}

// TestMoneyPaidIntoTheCompanyShowsUpInIt: the sign carries the direction, so
// money in needs no branch of its own.
func TestMoneyPaidIntoTheCompanyShowsUpInIt(t *testing.T) {
	f := loanTracker(t, "2026-07-31", 12400, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"c1","date":"2026-08-06","description":"From Rico","amount":-500,"account":"Company Checking","ignored":"paid in","movement":"owner_contribution"}]}`,
	}).ComputeMonth(context.Background(), 2026, time.August)
	bare := loanTracker(t, "2026-07-31", 12400, nil).ComputeMonth(context.Background(), 2026, time.August)

	if got := f.ActualCompanyClosingCents - bare.ActualCompanyClosingCents; got != 50000 {
		t.Errorf("the company gained %d, want the 500 that was paid in", got)
	}
}

// TestAnImportedMonthCarriesTheDrawIntoTheNextMonthsOpening: the closing
// figure seeds the next month, so a draw that only moved the month it landed
// in would be forgotten the moment the page turned.
func TestAnImportedMonthCarriesTheDrawIntoTheNextMonthsOpening(t *testing.T) {
	const drawn = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`
	const unmarked = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw"}]}`

	// September opens on what August closed at.
	marked := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": drawn}).
		ComputeMonth(context.Background(), 2026, time.September)
	plain := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": unmarked}).
		ComputeMonth(context.Background(), 2026, time.September)

	if got := plain.FundingPersonal.CompanyOpeningCents - marked.FundingPersonal.CompanyOpeningCents; got != 500000 {
		t.Errorf("September opened %d lower for the draw, want the 5000 August lost", got)
	}
}

// TestAHalfImportedMonthCarriesThePlanRatherThanHalfOfEach: the movements come
// from the same partial coverage the expenses do, and are no more trustworthy.
// Mixing a full month's declared tax with whatever part of it happened to be
// imported would charge the same money twice into a figure carried for good.
func TestAHalfImportedMonthCarriesThePlanRatherThanHalfOfEach(t *testing.T) {
	// Coverage stops on the 9th, so the month is present but not complete.
	const half = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-09","imported_at":"2026-08-10"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`

	partial := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": half}).
		ComputeMonth(context.Background(), 2026, time.September)
	none := loanTracker(t, "2026-07-31", 12400, nil).ComputeMonth(context.Background(), 2026, time.September)

	if partial.FundingPersonal.CompanyOpeningCents != none.FundingPersonal.CompanyOpeningCents {
		t.Errorf("a half-imported August carried %d into September and an unimported one carried %d — a half-read month closes on the plan, whole",
			partial.FundingPersonal.CompanyOpeningCents, none.FundingPersonal.CompanyOpeningCents)
	}
}

// TestWhatReachedTheOwnerIsARowRatherThanArithmeticToDoInYourHead: the whole
// complaint was a figure that did not add up on the page. Available to spend
// jumping by 5000 with nothing beside it saying why would be the same fault the
// other way round, so the row is shown and its label turns over with the
// direction rather than reading "drawn" for money paid in.
func TestWhatReachedTheOwnerIsARowRatherThanArithmeticToDoInYourHead(t *testing.T) {
	drawn := Figures{Currency: "€", ShowOpeningBalance: true, ShowActualBalance: true,
		OpeningBalanceCents: 420000, ArrivedPrivatelyCents: 500000, AvailableCents: 660000, ActualAvailableCents: 1160000}
	paidIn := drawn
	paidIn.ArrivedPrivatelyCents = -50000
	paidIn.ActualAvailableCents = 610000

	rec := httptest.NewRecorder()
	RenderPage(rec, drawn)
	body := rec.Body.String()
	if !strings.Contains(body, "Drawn from the company") {
		t.Error("Available to spend rose and the page does not say what raised it")
	}
	if !strings.Contains(body, "+5,000") {
		t.Error("the row lost its sign, so the direction is only in the colour")
	}
	// Both columns, so the planned figure stays readable beside it.
	if !strings.Contains(body, "planned 6,600") || !strings.Contains(body, "11,600") {
		t.Errorf("Available to spend does not show both columns:\n%s", body)
	}

	rec = httptest.NewRecorder()
	RenderPage(rec, paidIn)
	if got := rec.Body.String(); !strings.Contains(got, "Paid into the company") {
		t.Error("money paid into the company still reads as drawn out of it")
	}

	// A month with nothing crossing it earns no row at all.
	quiet := drawn
	quiet.ArrivedPrivatelyCents = 0
	rec = httptest.NewRecorder()
	RenderPage(rec, quiet)
	if strings.Contains(rec.Body.String(), "Drawn from the company") {
		t.Error("a month where nothing crossed still shows an empty row for it")
	}
}

// TestAnImportedMonthCarriesTheDrawIntoTheNextPrivateOpening is the twin of
// the company case above, on the other side of the same movement. A draw the
// owner still holds on the first of September is money September opens with, so
// forgetting it as the page turned would put the figure straight back where it
// was before any of this.
func TestAnImportedMonthCarriesTheDrawIntoTheNextPrivateOpening(t *testing.T) {
	const drawn = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`
	const unmarked = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw"}]}`

	marked := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": drawn}).
		ComputeMonth(context.Background(), 2026, time.September)
	plain := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": unmarked}).
		ComputeMonth(context.Background(), 2026, time.September)

	if got := marked.OpeningBalanceCents - plain.OpeningBalanceCents; got != 500000 {
		t.Errorf("September's private opening rose by %d for August's draw, want the 5000 drawn", got)
	}
}

// TestAHalfImportedMonthCarriesThePlanIntoThePrivateOpeningToo: partial
// coverage is exactly as untrustworthy for what reached the owner as for what
// he spent, and this figure is carried until the next bank reading. Folding
// half a month's draws into an otherwise planned figure would make it drift
// every time another week was imported, which is the mixture the company side
// already refuses. The viewed month is ungated on purpose — it is shown beside
// its planned twin rather than carried into anything.
func TestAHalfImportedMonthCarriesThePlanIntoThePrivateOpeningToo(t *testing.T) {
	// Coverage stops on the 9th, so the month is present but not complete.
	const half = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-09","imported_at":"2026-08-10"}],
		"transactions":[{"id":"d1","date":"2026-08-06","description":"To Rico","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`

	partial := loanTracker(t, "2026-07-31", 12400, map[string]string{"actuals/2026-08.json": half}).
		ComputeMonth(context.Background(), 2026, time.September)
	none := loanTracker(t, "2026-07-31", 12400, nil).ComputeMonth(context.Background(), 2026, time.September)

	if partial.OpeningBalanceCents != none.OpeningBalanceCents {
		t.Errorf("a half-imported August opened September at %d and an unimported one at %d — a half-read month closes on the plan, whole",
			partial.OpeningBalanceCents, none.OpeningBalanceCents)
	}
}

// TestTheCompanyIsWorthItsCashLessWhatItOwesTheOwner: a company holding
// nothing in the bank and owed a fortune by its owner is not a poor company,
// and the page has to be able to say both things at once.
func TestTheCompanyIsWorthItsCashLessWhatItOwesTheOwner(t *testing.T) {
	f := Figures{
		FundingPersonal:  PersonalView{ShowCompanyBalance: true, CompanyOpeningCents: 20400},
		LoanOpeningCents: -1700000, // the owner is overdrawn: he owes the company
	}
	f.publishCompanyWorth()

	if !f.ShowCompanyWorth {
		t.Fatal("a known cash figure and a known loan produce no worth")
	}
	if f.OwedToOwnerCents != 1700000 {
		t.Errorf("the company's side of the loan = %d, want the owner-centric figure flipped", f.OwedToOwnerCents)
	}
	if f.CompanyWorthCents != 1720400 {
		t.Errorf("worth = %d, want 204 in the bank plus the 17,000 owed to it", f.CompanyWorthCents)
	}
	if f.OwedLabel() != "Loan to the owner" {
		t.Errorf("label = %q, want it to say which way the debt runs", f.OwedLabel())
	}
}

// TestTheWorthRowFlipsTheLoansSignAndItsLabel: the loan is owner-centric, so
// the company's side of it is its negative, and a label that did not turn over
// with the sign would read as a lie half the time.
func TestTheWorthRowFlipsTheLoansSignAndItsLabel(t *testing.T) {
	owes := Figures{
		FundingPersonal:  PersonalView{ShowCompanyBalance: true, CompanyOpeningCents: 500000},
		LoanOpeningCents: 320000, // the company owes the owner
	}
	owes.publishCompanyWorth()

	if owes.OwedToOwnerCents != -320000 {
		t.Errorf("owed = %d, want a liability on the company's side", owes.OwedToOwnerCents)
	}
	if owes.CompanyWorthCents != 180000 {
		t.Errorf("worth = %d, want the cash less what it owes", owes.CompanyWorthCents)
	}
	if owes.OwedLabel() != "Loan from the owner" {
		t.Errorf("label = %q, want the other direction", owes.OwedLabel())
	}
}

// TestNoWorthTotalWithoutBothACashFigureAndAKnownLoan: "not known" is not
// zero, and a total made from one figure and a guess is worse than no total.
func TestNoWorthTotalWithoutBothACashFigureAndAKnownLoan(t *testing.T) {
	noCash := Figures{LoanOpeningCents: -1700000}
	noCash.publishCompanyWorth()
	if noCash.ShowCompanyWorth {
		t.Error("a worth total with no company balance to build it from")
	}

	noLoan := Figures{
		FundingPersonal:     PersonalView{ShowCompanyBalance: true, CompanyOpeningCents: 20400},
		DirectorLoanUnknown: "nobody has said",
	}
	noLoan.publishCompanyWorth()
	if noLoan.ShowCompanyWorth {
		t.Error("a worth total built on a loan nobody has stated")
	}
}

// TestTheWorthTotalIsShownInAMonthTheAccountRowsAreNot: the per-account rows
// stop at the month after the newest bank reading, because after that they no
// longer sum to the figure above them. The loan is walked to the viewed month
// like the cash is, so it does not share that limit — and hiding it would hide
// it in exactly the months where the confusion is worst.
func TestTheWorthTotalIsShownInAMonthTheAccountRowsAreNot(t *testing.T) {
	// The reading opens August, so September is a month past it.
	f := loanTracker(t, "2026-07-31", -1700000/100, nil).ComputeMonth(context.Background(), 2026, time.September)

	if len(f.CompanyAccounts) != 0 {
		t.Fatalf("the fixture is wrong: September still lists account rows (%d)", len(f.CompanyAccounts))
	}
	if !f.ShowCompanyWorth {
		t.Error("the worth total is hidden in a month whose account rows are, though the loan is walked to it")
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()
	worth := strings.Index(body, "The company is worth")
	cascade := strings.Index(body, `class="label">Employer social`)
	if worth < 0 || cascade < 0 {
		t.Fatalf("worth at %d, cascade at %d — one is missing", worth, cascade)
	}
	if worth > cascade {
		t.Error("the worth total is rendered below the cascade it is not part of")
	}
}

func dualAnchorTracker(t *testing.T, bankAsOf, loanAsOf string) *Tracker {
	t.Helper()
	trk := actualsTracker(t, nil)
	trk.Accounts = newTestAccounts(t, fmt.Sprintf(`{"accounts":[
		{"name":"Private Checking","kind":"private","balances":[{"as_of":%q,"balance":4200}]},
		{"name":"Company Checking","kind":"company","balances":[{"as_of":%q,"balance":6800}]}
	],"director_loan":{"balances":[{"as_of":%q,"balance":12400}]}}`, bankAsOf, bankAsOf, loanAsOf))
	return trk
}

func TestAMonthBeforeTheBankReadingHasNoBalance(t *testing.T) {
	ctx := context.Background()

	early := dualAnchorTracker(t, "2026-07-31", "2025-12-31").ComputeMonth(ctx, 2026, time.September)

	together := dualAnchorTracker(t, "2026-07-31", "2026-07-31").ComputeMonth(ctx, 2026, time.September)

	if early.AccountsErr != "" {
		t.Fatalf("accounts error: %s", early.AccountsErr)
	}

	if early.LoanClosingCents == together.LoanClosingCents {
		return
	}
	drift := early.LoanClosingCents - together.LoanClosingCents
	if drift > 0 {
		t.Errorf("the loan closes %d cents higher when anchored earlier (%d vs %d) — the pre-anchor months were cascaded with a balance from the future",
			drift, early.LoanClosingCents, together.LoanClosingCents)
	}
}

func TestThePreAnchorMonthsSeeNoCompanyBalance(t *testing.T) {
	ctx := context.Background()
	trk := dualAnchorTracker(t, "2026-07-31", "2025-12-31")

	march := trk.ComputeMonth(ctx, 2026, time.March)
	if march.AccountsErr != "" {
		t.Fatalf("accounts error: %s", march.AccountsErr)
	}
	if march.FundingPersonal.CompanyOpeningCents != 0 {
		t.Errorf("March opens with %d cents of company money, want 0 — the reading only opens in August",
			march.FundingPersonal.CompanyOpeningCents)
	}

	august := trk.ComputeMonth(ctx, 2026, time.August)
	if august.FundingPersonal.CompanyOpeningCents != 680000 {
		t.Errorf("August opens with %d cents, want the 6 800 read on 31 July", august.FundingPersonal.CompanyOpeningCents)
	}
}
