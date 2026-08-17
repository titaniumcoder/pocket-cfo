package tracker

import (
	"context"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestADividendIsFoundByItsMonthAndNotItsDay: the day on a dividend is
// informational, the same convention a one-off category's date follows. What
// decides which month is charged is the month, so a distribution dated on the
// 30th and one dated on the 1st of the same month are the same month's money.
func TestADividendIsFoundByItsMonthAndNotItsDay(t *testing.T) {
	trk := actualsTracker(t, nil)
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": `{"groups":[
		{"name":"Housing","kind":"private","categories":[{"id":"00000000-0000-4000-8000-000000000001","name":"Rent","amount":1000}]}
	],"dividends":[{"date":"2026-09-30","amount":10000,"note":"2025 profit"}]}`})

	bv, err := trk.Budget.ForMonth(context.Background(), 2026, time.September, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}

	if due := bv.Dividends.dueIn(yearMonth{2026, time.September}); due.AmountEUR != 10000 {
		t.Errorf("September is due %v, want the 10000 dated on its 30th", due.AmountEUR)
	}
	if due := bv.Dividends.dueIn(yearMonth{2026, time.October}); due.AmountEUR != 0 {
		t.Errorf("October is due %v — the dividend belongs to September alone", due.AmountEUR)
	}
	// The plan travels whole, not pre-filtered: the roll-forward walks other
	// months with this same view and each one picks its own out.
	bvAugust, err := trk.Budget.ForMonth(context.Background(), 2026, time.August, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if due := bvAugust.Dividends.dueIn(yearMonth{2026, time.September}); due.AmountEUR != 10000 {
		t.Error("August's view cannot see September's dividend, so a walked month would close the company too high")
	}
}

// TestTwoDividendsInOneMonthAreSummed: the file states what was distributed,
// and the month is charged the whole of it — with both days kept, so the page
// can be reconciled line by line against the file.
func TestTwoDividendsInOneMonthAreSummed(t *testing.T) {
	trk := actualsTracker(t, nil)
	trk.Budget = newTestBudget(t, map[string]string{"budget.json": `{"groups":[
		{"name":"Housing","kind":"private","categories":[{"id":"00000000-0000-4000-8000-000000000001","name":"Rent","amount":1000}]}
	],"dividends":[{"date":"2026-09-30","amount":4000},{"date":"2026-09-15","amount":6000}]}`})

	bv, err := trk.Budget.ForMonth(context.Background(), 2026, time.September, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	due := bv.Dividends.dueIn(yearMonth{2026, time.September})
	if due.AmountEUR != 10000 {
		t.Errorf("September is due %v, want 4000 and 6000 summed", due.AmountEUR)
	}
	// Sorted by date, so the page does not reorder itself when the file does.
	rows := due.dividendRows()
	if len(rows) != 2 || rows[0].Day != "15.09.2026" || rows[1].Day != "30.09.2026" {
		t.Errorf("rows = %+v, want both, earliest first", rows)
	}
	if rows[0].Cents != 600000 || rows[1].Cents != 400000 {
		t.Errorf("rows = %+v, want each entry's own amount so the total can be read back", rows)
	}
}

// TestAnEmptyDividendChangesNoFigure guards the commit that threaded a
// distribution through the cascade without paying one: every figure in a month
// that has no dividend must be cent-for-cent what it was before the parameter
// existed, or the plumbing itself moved money.
func TestAnEmptyDividendChangesNoFigure(t *testing.T) {
	p := params()
	r := p.rulesFor(testMonth)
	stock := companyStock{Known: true, OpeningCents: 500000}

	plain := p.breakdown(6000, 250, 1, r, SalaryDecision{Mode: SalaryFull}, stock, noDividend)

	// The same month, reached through a plan that holds a distribution in a
	// month this one is not.
	elsewhere := p.withDividends(Dividends{{On: yearMonth{2030, time.March}, Day: "2030-03-31", AmountEUR: 10000}})
	viaPlan := elsewhere.breakdown(6000, 250, 1, r, SalaryDecision{Mode: SalaryFull}, stock,
		elsewhere.Dividends.dueIn(testMonth))

	if !reflect.DeepEqual(plain, viaPlan) {
		t.Errorf("a month with no dividend differs once a plan exists:\n plain = %+v\n plan  = %+v", plain, viaPlan)
	}
	for _, zero := range []struct {
		name  string
		cents int
	}{
		{"dividend", plain.DividendCents},
		{"dividend tax", plain.DividendTaxCents},
		{"company profit tax", plain.CompanyProfitTaxCents},
	} {
		if zero.cents != 0 {
			t.Errorf("%s = %d in a month with no distribution", zero.name, zero.cents)
		}
	}
}

// withDividendRates is the test fixture's legislation plus the two rates a
// distribution is charged at, at Bulgaria's real figures.
func withDividendRates(p PersonalParams) PersonalParams {
	p.Legislation = append(Legislation{}, p.Legislation...)
	p.Legislation[0].CompanyProfitTax = Bands{{From: 0, Rate: 0.10}}
	p.Legislation[0].DividendTax = Bands{{From: 0, Rate: 0.05}}
	return p
}

func dividendOf(amount float64, day string, on yearMonth) dividendDue {
	return Dividends{{On: on, Day: day, AmountEUR: amount}}.dueIn(on)
}

// TestADividendOfTenThousandCostsTheCompanyElevenThousandButOnlyInWorth: the
// distribution costs the company 11,000 — the gross plus the profit tax it
// triggers — but only 1,500 of that is money leaving the bank. The other 9,500
// is handed to the owner as a claim, and the director's loan is what carries
// it. Getting this wrong is what made a distribution look unaffordable to a
// company whose cash had already gone out as draws.
func TestADividendOfTenThousandCostsTheCompanyElevenThousandButOnlyInWorth(t *testing.T) {
	p := withDividendRates(params())
	r := p.rulesFor(testMonth)
	stock := companyStock{Known: true, OpeningCents: 5000000}
	d := SalaryDecision{Mode: SalaryFixed, FixedEUR: 2500}

	without := p.breakdown(0, 0, 1, r, d, stock, noDividend)
	with := p.breakdown(0, 0, 1, r, d, stock, dividendOf(10000, "2026-01-31", testMonth))

	if with.DividendCents != 1000000 {
		t.Errorf("dividend = %d, want 10000", with.DividendCents)
	}
	if with.CompanyProfitTaxCents != 100000 {
		t.Errorf("company profit tax = %d, want 10%% of the distribution", with.CompanyProfitTaxCents)
	}
	if with.DividendTaxCents != 50000 {
		t.Errorf("dividend tax = %d, want 5%% of the distribution", with.DividendTaxCents)
	}

	cash := without.CompanyClosingCents - with.CompanyClosingCents
	if cash != 150000 {
		t.Errorf("the company's cash fell by %d, want only the two taxes — the gross is a claim, not a payment", cash)
	}
	owed := with.NetIncomeCents - without.NetIncomeCents
	if owed != 950000 {
		t.Errorf("the owner is owed %d more, want the dividend less its dividend tax", owed)
	}
	// Cash out plus the claim handed over is what the distribution really cost.
	if cash+owed != 1100000 {
		t.Errorf("the distribution cost %d in all, want the gross plus its profit tax", cash+owed)
	}
}

// TestADividendSettledAgainstTheLoanTakesNoCashOutOfTheCompany is the case that
// prompted the correction: a company holding almost nothing can still declare
// the distribution that clears what its owner already drew, because the gross
// never moves. Only the taxes have to be found.
func TestADividendSettledAgainstTheLoanTakesNoCashOutOfTheCompany(t *testing.T) {
	p := withDividendRates(params())
	r := p.rulesFor(testMonth)
	// 204 in the bank, the way it really was.
	stock := companyStock{Known: true, OpeningCents: 20400}
	d := SalaryDecision{Mode: SalaryNone}

	v := p.breakdown(0, 0, 1, r, d, stock, dividendOf(17894.74, "2026-01-31", testMonth))

	taxes := v.CompanyProfitTaxCents + v.DividendTaxCents
	if got := stock.OpeningCents - v.CompanyClosingCents; got != taxes {
		t.Errorf("the company's cash fell by %d, want the %d of tax and nothing else", got, taxes)
	}
	// It is still overdrawn — the taxes are real money it does not have — but
	// by 2,684, not by 20,579.
	if v.CompanyClosingCents < -300000 {
		t.Errorf("closing = %d; the gross is being taken out of the bank again", v.CompanyClosingCents)
	}
}

// TestTheClosingBalanceStillEqualsTheRowsAboveItWithADividend: the closing
// figure seeds the next month and the page has to add up to it, which is the
// first thing a reader checks.
func TestTheClosingBalanceStillEqualsTheRowsAboveItWithADividend(t *testing.T) {
	p := withDividendRates(params())
	r := p.rulesFor(testMonth)
	stock := companyStock{Known: true, OpeningCents: 5000000}

	for _, mode := range []SalaryDecision{{Mode: SalaryFull}, {Mode: SalaryFixed, FixedEUR: 2500}, {Mode: SalaryNone}} {
		v := p.breakdown(4000, 250, 1, r, mode, stock, dividendOf(10000, "2026-01-31", testMonth))

		// The company's column: cash out only, so the gross is not in it.
		rows := stock.OpeningCents + v.CompanyIncomeCents - v.CompanyExpensesCents -
			v.EmployerContribCents - v.GrossSalaryCents - v.DividendTaxCents - v.CompanyProfitTaxCents
		if rows != v.CompanyClosingCents {
			t.Errorf("%s: the rows add to %d and the closing figure says %d", mode.Mode, rows, v.CompanyClosingCents)
		}

		personal := v.GrossSalaryCents - v.EmployeeContribCents - v.IncomeTaxCents +
			v.DividendCents - v.DividendTaxCents
		if personal != v.NetIncomeCents {
			t.Errorf("%s: the personal rows add to %d and net income says %d", mode.Mode, personal, v.NetIncomeCents)
		}
	}
}

// TestAFullSalaryStillClosesTheCompanyAtItsTargetWithADividend is why the
// distribution is charged before the salary is sized rather than only at the
// close. A full salary takes the whole remainder, so subtracting the dividend
// afterwards would drain the company straight through the floor targetBalance
// is documented to be.
func TestAFullSalaryStillClosesTheCompanyAtItsTargetWithADividend(t *testing.T) {
	p := withDividendRates(params())
	r := p.rulesFor(testMonth)
	stock := companyStock{Known: true, OpeningCents: 5000000, TargetCents: 1500000}

	v := p.breakdown(4000, 0, 1, r, SalaryDecision{Mode: SalaryFull}, stock,
		dividendOf(10000, "2026-01-31", testMonth))

	if v.CompanyClosingCents != stock.TargetCents {
		t.Errorf("a full salary closed the company at %d, want its target of %d — the dividend went through the floor",
			v.CompanyClosingCents, stock.TargetCents)
	}
}

// TestADividendShrinksWhatAFullSalaryCanAfford is the same rule seen from the
// other side: the money is the same money.
func TestADividendShrinksWhatAFullSalaryCanAfford(t *testing.T) {
	p := withDividendRates(params())
	r := p.rulesFor(testMonth)
	stock := companyStock{Known: true, OpeningCents: 5000000}

	without := p.breakdown(4000, 0, 1, r, SalaryDecision{Mode: SalaryFull}, stock, noDividend)
	with := p.breakdown(4000, 0, 1, r, SalaryDecision{Mode: SalaryFull}, stock,
		dividendOf(10000, "2026-01-31", testMonth))

	if with.GrossSalaryCents >= without.GrossSalaryCents {
		t.Errorf("gross was %d without a distribution and %d with one — a dividend the company has promised cannot also fund payroll",
			without.GrossSalaryCents, with.GrossSalaryCents)
	}
}

// TestAFixedSalaryIgnoresTheDividendAndOverdrawsInstead: fixed is the one mode
// documented to outrank affordability, and it keeps doing so.
func TestAFixedSalaryIgnoresTheDividendAndOverdrawsInstead(t *testing.T) {
	p := withDividendRates(params())
	r := p.rulesFor(testMonth)
	stock := companyStock{Known: true, OpeningCents: 100000}
	d := SalaryDecision{Mode: SalaryFixed, FixedEUR: 2500}

	without := p.breakdown(0, 0, 1, r, d, stock, noDividend)
	with := p.breakdown(0, 0, 1, r, d, stock, dividendOf(10000, "2026-01-31", testMonth))

	if with.GrossSalaryCents != without.GrossSalaryCents {
		t.Errorf("a fixed salary shrank from %d to %d because of a dividend", without.GrossSalaryCents, with.GrossSalaryCents)
	}
	if with.CompanyClosingCents >= 0 {
		t.Errorf("the company closed at %d — a fixed salary and a dividend it cannot cover must overdraw visibly", with.CompanyClosingCents)
	}
}

// TestADividendWithNoRateInForceIsRefusedOutLoud: everywhere else an
// un-legislated month is charged nothing and says so, because a salary happens
// in every month. A dividend exists only where somebody wrote one, so charging
// it at 0% and 0% would be a wrong figure wearing a right one's clothes.
func TestADividendWithNoRateInForceIsRefusedOutLoud(t *testing.T) {
	p := params() // no dividend rates at all
	p.Dividends = Dividends{{On: testMonth, Day: "2026-01-31", AmountEUR: 10000}}

	v := p.breakdownMonths([]float64{4000}, []float64{0}, testMonth, companyStock{Known: true})
	if v.Err == "" {
		t.Fatal("a dividend in a month with no rate in force was charged rather than refused")
	}
	for _, want := range []string{"dividend", "companyProfitTax"} {
		if !strings.Contains(v.Err, want) {
			t.Errorf("the refusal %q never says %q", v.Err, want)
		}
	}

	// And a month with no distribution is untouched by the missing rates.
	quiet := params().breakdownMonths([]float64{4000}, []float64{0}, testMonth, companyStock{Known: true})
	if quiet.Err != "" {
		t.Errorf("a month with no dividend was refused for lacking dividend rates: %s", quiet.Err)
	}
}

// TestCompanyProfitTaxSitsAboveTheCascadeAndDividendTaxBesideIncomeTax pins
// the ordering, which is the half of this feature that is a reading decision
// rather than an arithmetic one. Company profit tax is a company cost and must
// not read as reducing net income, so it sits with the company's own costs
// above the payroll cascade. The dividend behaves like a gross and its tax
// like income tax, so the pair brackets the personal rows: gross and dividend
// in, employee social, income tax and dividend tax out, net income at the end.
func TestCompanyProfitTaxSitsAboveTheCascadeAndDividendTaxBesideIncomeTax(t *testing.T) {
	f := Figures{Currency: "€", FundingPersonal: PersonalView{
		ShowCompanyBalance:    true,
		GrossSalaryCents:      250000,
		NetIncomeCents:        1148807,
		DividendCents:         1000000,
		DividendTaxCents:      50000,
		CompanyProfitTaxCents: 100000,
		DividendRows:          []DividendRow{{Day: "30.09.2026", Cents: 1000000}},
		DividendTaxRate:       []RateLine{{Rate: "5%"}},
		CompanyProfitTaxRate:  []RateLine{{Rate: "10%"}},
	}}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	at := func(label string) int {
		i := strings.Index(body, `class="label">`+label)
		if i < 0 {
			t.Fatalf("the page never says %q", label)
		}
		return i
	}
	order := []string{"Company profit tax", "Employer social", "Gross salary", "Dividend <small>", "Employee social", "Income tax", "Dividend tax", "Net income", "Left in the company"}
	for i := 1; i < len(order); i++ {
		if at(order[i-1]) >= at(order[i]) {
			t.Errorf("%q is rendered after %q", order[i-1], order[i])
		}
	}

	// A single distribution dates the row itself rather than repeating itself
	// in a sub-row.
	if !strings.Contains(body, "<small>(30.09.2026)</small>") {
		t.Error("the Dividend row does not say which day it was paid")
	}
	// Both rates fold on a narrow screen the way every other rate does, which
	// is the payoff for expressing them as bands.
	for _, rate := range []string{"5%", "10%"} {
		if strings.Count(body, rate) < 2 {
			t.Errorf("rate %q appears once — it needs the mid cell and the narrow-screen fold", rate)
		}
	}
}

// TestAMonthWithoutADividendShowsNoDividendRows: a zero in three extra rows
// reads as something having gone wrong, so a quiet month looks exactly as it
// did before dividends existed.
func TestAMonthWithoutADividendShowsNoDividendRows(t *testing.T) {
	f := Figures{Currency: "€", FundingPersonal: PersonalView{GrossSalaryCents: 250000}}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	for _, gone := range []string{"Company profit tax", "Dividend tax", `class="label">Dividend`} {
		if strings.Contains(body, gone) {
			t.Errorf("a month with no distribution still renders %q", gone)
		}
	}
}

// TestTwoDividendsInOneMonthAreListedSeparately: the total has to be readable
// back against budget.json, and a single figure covering two entries is not.
func TestTwoDividendsInOneMonthAreListedSeparately(t *testing.T) {
	f := Figures{Currency: "€", FundingPersonal: PersonalView{
		DividendCents: 1000000,
		DividendRows:  []DividendRow{{Day: "15.09.2026", Cents: 600000}, {Day: "30.09.2026", Cents: 400000}},
	}}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	for _, want := range []string{"15.09.2026", "30.09.2026"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page never names the distribution dated %s", want)
		}
	}
	if strings.Contains(body, "<small>(15.09.2026)</small>") {
		t.Error("two distributions dated the Dividend row with one of them")
	}
}

// TestTheDividendRatesSurviveTheMonthAggregation: the ledger renders the view
// breakdownMonths returns, not the one breakdown built, so a rate that is not
// carried across that boundary is computed correctly and then shown as blank.
// That is precisely what happened, and only the rendered page revealed it.
func TestTheDividendRatesSurviveTheMonthAggregation(t *testing.T) {
	p := withDividendRates(params())
	p.Dividends = Dividends{{On: testMonth, Day: "2026-01-31", AmountEUR: 10000}}

	one := p.breakdownMonths([]float64{4000}, []float64{0}, testMonth, companyStock{Known: true, OpeningCents: 5000000})
	if len(one.DividendTaxRate) == 0 || len(one.CompanyProfitTaxRate) == 0 {
		t.Errorf("a single month lost its dividend rates: tax=%v profit=%v", one.DividendTaxRate, one.CompanyProfitTaxRate)
	}

	year := p.breakdownMonths(make([]float64, 12), make([]float64, 12), testMonth, companyStock{Known: true, OpeningCents: 5000000})
	if len(year.DividendTaxRate) == 0 || len(year.CompanyProfitTaxRate) == 0 {
		t.Errorf("a year holding a distribution lost its dividend rates: tax=%v profit=%v", year.DividendTaxRate, year.CompanyProfitTaxRate)
	}
	if year.DividendCents != 1000000 {
		t.Errorf("the year summed the distribution to %d, want it counted once", year.DividendCents)
	}

	// A year with no distribution names no rates, or the ledger would carry
	// two rate labels with nothing to charge.
	quiet := withDividendRates(params()).breakdownMonths(make([]float64, 12), make([]float64, 12), testMonth, companyStock{Known: true})
	if len(quiet.DividendTaxRate) != 0 || len(quiet.CompanyProfitTaxRate) != 0 {
		t.Error("a year with no distribution still labels the dividend rates")
	}
}

func TestAnUnratedMonthReportsNoTaxRatherThanZeroTax(t *testing.T) {
	unrated := params()
	d := Dividends{{On: testMonth, Day: "2026-01-31", AmountEUR: 10000}}

	got := unrated.DividendsIn(d, testMonth.Year, testMonth.Month)
	if len(got) != 1 {
		t.Fatalf("got %d reports, want 1", len(got))
	}
	r := got[0]
	if r.Unrated == "" {
		t.Error("the report charged a dividend in a month with no rate in force and said nothing about it")
	}
	if r.CompanyProfitTaxCents != 0 || r.DividendTaxCents != 0 {
		t.Errorf("taxes = %d and %d, want them left at zero beside the note rather than computed",
			r.CompanyProfitTaxCents, r.DividendTaxCents)
	}
	if r.GrossCents != 1000000 {
		t.Errorf("gross = %d, want the stated 10 000", r.GrossCents)
	}
	if r.NetToOwnerCents != 0 || r.CashNeededCents != 0 {
		t.Error("net to owner and cash needed were derived from taxes that do not exist")
	}

	rated := withDividendRates(params()).DividendsIn(d, testMonth.Year, testMonth.Month)
	if len(rated) != 1 || rated[0].Unrated != "" {
		t.Fatalf("a legislated month reported unrated: %+v", rated)
	}
	if rated[0].DividendTaxCents == 0 {
		t.Error("a legislated month charged no dividend tax")
	}
}
