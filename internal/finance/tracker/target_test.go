package tracker

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseTargetPlan(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries []TargetEntry
		wantErr string // substring; "" means valid
	}{
		{"a closed range", []TargetEntry{{From: "2026-04", To: "2026-12", Amount: f64(20000)}}, ""},
		{"open ended", []TargetEntry{{From: "2026-04", Amount: f64(20000)}}, ""},
		{"a full date is a month", []TargetEntry{{From: "2026-04-01", Amount: f64(20000)}}, ""},
		{"no entries at all", nil, ""},
		{"no amount", []TargetEntry{{From: "2026-04"}}, "names no amount"},
		{"a target of nothing", []TargetEntry{{From: "2026-04", Amount: f64(0)}}, "always already met"},
		{"a negative target", []TargetEntry{{From: "2026-04", Amount: f64(-1)}}, "always already met"},
		{"unparseable from", []TargetEntry{{From: "April", Amount: f64(1)}}, "is not a month"},
		{"unparseable to", []TargetEntry{{From: "2026-04", To: "later", Amount: f64(1)}}, "is not a month"},
		{"to before from", []TargetEntry{{From: "2026-06", To: "2026-04", Amount: f64(1)}}, "precedes"},
		{
			"two figures for one month",
			[]TargetEntry{{From: "2026-04", Amount: f64(1)}, {From: "2026-04", Amount: f64(2)}},
			"two figures for",
		},
		{
			"ranges that overlap",
			[]TargetEntry{{From: "2026-04", To: "2026-08", Amount: f64(1)}, {From: "2026-06", Amount: f64(2)}},
			"overlapping",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTargetPlan(tt.entries)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("rejected a valid plan: %v", err)
			case tt.wantErr == "":
			case err == nil:
				t.Fatalf("accepted, want a complaint about %q", tt.wantErr)
			case !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("err = %v, want one about %q", err, tt.wantErr)
			}
		})
	}
}

// TestTargetAtCarriesForwardAndFallsBack mirrors the salary block's resolution
// exactly, so the two dated lists cannot come to disagree about what a range
// with no `to` means.
func TestTargetAtCarriesForwardAndFallsBack(t *testing.T) {
	plan, err := ParseTargetPlan([]TargetEntry{
		{From: "2026-04", To: "2026-06", Amount: f64(10000)},
		{From: "2026-09", Amount: f64(20000)},
		{From: "2027-02", Amount: f64(30000)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		ym       yearMonth
		want     float64
		inForce  bool
		whatFrom string
	}{
		{yearMonth{2026, time.March}, 0, false, "before anything"},
		{yearMonth{2026, time.April}, 10000, true, "first covered"},
		{yearMonth{2026, time.June}, 10000, true, "last covered, inclusive"},
		{yearMonth{2026, time.July}, 0, false, "the gap after a closed range"},
		{yearMonth{2026, time.September}, 20000, true, "open-ended starts"},
		{yearMonth{2027, time.January}, 20000, true, "carries across a year boundary"},
		{yearMonth{2027, time.February}, 30000, true, "until the next entry"},
		{yearMonth{2030, time.June}, 30000, true, "and on"},
	} {
		got, inForce := plan.at(tt.ym)
		if got != tt.want || inForce != tt.inForce {
			t.Errorf("%s (%s) = %v/%v, want %v/%v", tt.ym, tt.whatFrom, got, inForce, tt.want, tt.inForce)
		}
	}
}

// TestTheTargetIsAFloorAndNotAWatermark is the whole reason the target is
// subtracted from what payroll may spend rather than only switching the mode.
// If a full salary drained the balance, the month after reaching the target
// would fall under it again and pay the minimum — the figure would oscillate
// forever and never be a reserve. Reaching it must be stable.
func TestTheTargetIsAFloorAndNotAWatermark(t *testing.T) {
	p := bulgariaBands()
	plan, err := ParseTargetPlan([]TargetEntry{{From: "2026-01", Amount: f64(3000)}})
	if err != nil {
		t.Fatal(err)
	}
	p.Target = plan
	r := p.rulesFor(yearMonth{2026, time.July})
	ym := yearMonth{2026, time.July}

	// Under the target: held at the minimum whatever the month earned.
	under := companyStock{Known: true, OpeningCents: 100000, TargetCents: 300000}
	d := p.decide(ym, under)
	if d.Mode != SalaryMinimum || !d.HeldForTarget {
		t.Errorf("decision under target = %+v, want a minimum held for the target", d)
	}
	held := p.breakdown(6000, 0, 1, r, d, under)
	if held.GrossSalaryCents != 62020 {
		t.Errorf("gross = %d, want the 620.20 minimum", held.GrossSalaryCents)
	}
	if held.CompanyClosingCents <= under.OpeningCents {
		t.Errorf("closing = %d, opened at %d — a held month has to build the balance up",
			held.CompanyClosingCents, under.OpeningCents)
	}

	// At the target: full salary resumes, and stops at the target rather than
	// spending through it.
	at := companyStock{Known: true, OpeningCents: 300000, TargetCents: 300000}
	if got := p.decide(ym, at); got.Mode != SalaryFull || got.HeldForTarget {
		t.Errorf("decision at target = %+v, want a plain full salary", got)
	}
	resumed := p.breakdown(6000, 0, 1, r, p.decide(ym, at), at)
	if resumed.CompanyClosingCents != 300000 {
		t.Errorf("closing = %d, want exactly the 3,000 target — a full salary must not spend the reserve", resumed.CompanyClosingCents)
	}
	if resumed.GrossSalaryCents <= held.GrossSalaryCents {
		t.Error("reaching the target bought no more salary than being under it")
	}

	// And the month after is still full, because the balance never fell back
	// below the target. This is the oscillation the floor prevents.
	next := companyStock{Known: true, OpeningCents: resumed.CompanyClosingCents, TargetCents: 300000}
	if got := p.decide(ym.addMonths(1), next); got.Mode != SalaryFull {
		t.Errorf("the month after reaching the target = %q, want full — the target is oscillating", got.Mode)
	}

	// Above the target: only the excess is spendable.
	above := companyStock{Known: true, OpeningCents: 500000, TargetCents: 300000}
	if got := above.spendableEUR(); got != 2000 {
		t.Errorf("spendable = %v, want 2000 — the excess over the target and no more", got)
	}
}

// TestATargetNeedsABalanceToCompareAgainst: "we were not told what the company
// holds" is not "the company holds nothing". Treating it as zero would hold
// every month at the minimum on no evidence — including the year view, which
// reads no balances at all.
func TestATargetNeedsABalanceToCompareAgainst(t *testing.T) {
	p := bulgariaBands()
	plan, err := ParseTargetPlan([]TargetEntry{{From: "2026-01", Amount: f64(3000)}})
	if err != nil {
		t.Fatal(err)
	}
	p.Target = plan
	ym := yearMonth{2026, time.July}

	unknown := p.targetStock(ym, companyStock{})
	if unknown.TargetCents != 0 {
		t.Errorf("TargetCents = %d on an unknown balance, want 0", unknown.TargetCents)
	}
	if got := p.decide(ym, unknown); got.Mode != SalaryFull || got.HeldForTarget {
		t.Errorf("decision = %+v on an unknown balance, want an untouched full salary", got)
	}

	// A balance that is genuinely zero is a figure, and it is under target.
	zero := p.targetStock(ym, companyStock{Known: true})
	if zero.TargetCents != 300000 {
		t.Errorf("TargetCents = %d on a known balance, want 300000", zero.TargetCents)
	}
	if got := p.decide(ym, zero); got.Mode != SalaryMinimum {
		t.Errorf("decision = %+v on a known-empty company, want the minimum", got)
	}
}

// TestATargetCannotMakeAMonthPayMore: the salary block is the more explicit
// statement, so an explicit minimum, none or fixed month wins. The target is
// then recorded as idle rather than silently doing nothing.
func TestATargetCannotMakeAMonthPayMore(t *testing.T) {
	p := bulgariaBands()
	target, err := ParseTargetPlan([]TargetEntry{{From: "2026-01", Amount: f64(3000)}})
	if err != nil {
		t.Fatal(err)
	}
	p.Target = target
	ym := yearMonth{2026, time.July}
	under := companyStock{Known: true, OpeningCents: 0, TargetCents: 300000}

	for _, tt := range []struct {
		mode   SalaryMode
		amount *float64
	}{
		{SalaryNone, nil},
		{SalaryMinimum, nil},
		{SalaryFixed, f64(2500)},
	} {
		salary, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-01", Mode: string(tt.mode), Amount: tt.amount}})
		if err != nil {
			t.Fatal(err)
		}
		p.Salary = salary
		got := p.decide(ym, under)
		if got.Mode != tt.mode {
			t.Errorf("a target over an explicit %q month changed it to %q", tt.mode, got.Mode)
		}
		if !got.TargetIdle {
			t.Errorf("a target over an explicit %q month is doing nothing and does not say so", tt.mode)
		}
		if got.HeldForTarget {
			t.Errorf("an explicit %q month reports itself as held for the target", tt.mode)
		}
	}
}

// TestIdleTargetMonthsAreReportedNotRefused: the user is the authority, so a
// target over a fixed period loads. But it visibly does nothing, and /info is
// where that has to be findable.
func TestIdleTargetMonthsAreReportedNotRefused(t *testing.T) {
	salary, err := ParseSalaryPlan([]SalaryEntry{{From: "2026-05", To: "2026-06", Mode: "fixed", Amount: f64(2500)}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := ParseTargetPlan([]TargetEntry{{From: "2026-04", To: "2026-07", Amount: f64(20000)}})
	if err != nil {
		t.Fatal(err)
	}
	idle := ValidateTargetAgainstSalary(target, salary)
	if len(idle) != 2 {
		t.Fatalf("idle months = %v, want the two fixed ones", idle)
	}
	for i, want := range []string{"2026-05 (salary is fixed then)", "2026-06 (salary is fixed then)"} {
		if idle[i] != want {
			t.Errorf("idle[%d] = %q, want %q", i, idle[i], want)
		}
	}

	// A target entirely over full-salary months has nothing to report.
	if got := ValidateTargetAgainstSalary(target, nil); len(got) != 0 {
		t.Errorf("idle months = %v over a plan that pays full throughout, want none", got)
	}

	// An open-ended target has to be walked as far as the salary block says
	// anything, not just its own first month — past that every month is full,
	// which is where a target does apply.
	open, err := ParseTargetPlan([]TargetEntry{{From: "2026-04", Amount: f64(20000)}})
	if err != nil {
		t.Fatal(err)
	}
	got := ValidateTargetAgainstSalary(open, salary)
	if len(got) != 2 {
		t.Errorf("idle months = %v, want both fixed months from an open-ended target", got)
	}
}

// TestATargetWithoutAMinimumWageIsRefused: holding a month "at the minimum"
// where the minimum is zero pays nothing at all, which is not what a target
// asks for and would never say so.
func TestATargetWithoutAMinimumWageIsRefused(t *testing.T) {
	l, err := ParseLegislation([]LegislationEntry{{From: "2026-07", MinimumWage: f64(1077)}})
	if err != nil {
		t.Fatal(err)
	}
	early, err := ParseTargetPlan([]TargetEntry{{From: "2026-05", To: "2026-08", Amount: f64(20000)}})
	if err != nil {
		t.Fatal(err)
	}
	err = RequireMinimumWageForTargets(early, l)
	if err == nil {
		t.Fatal("accepted a target over months with no minimum wage in force")
	}
	if !strings.Contains(err.Error(), "May 2026") {
		t.Errorf("err = %v, want the month named", err)
	}

	ok, err := ParseTargetPlan([]TargetEntry{{From: "2026-07", To: "2026-08", Amount: f64(20000)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireMinimumWageForTargets(ok, l); err != nil {
		t.Errorf("rejected a target that has a minimum wage throughout: %v", err)
	}
}

// TestTheTargetWalksTheCompanyUpToItAndHoldsThere is the scenario end to end:
// the balance builds while held at the minimum, and once the target is reached
// full salary resumes and the balance sits on the target instead of collapsing.
func TestTheTargetWalksTheCompanyUpToItAndHoldsThere(t *testing.T) {
	p := bulgariaBands()
	target, err := ParseTargetPlan([]TargetEntry{{From: "2026-01", Amount: f64(4000)}})
	if err != nil {
		t.Fatal(err)
	}
	p.Target = target

	// Twelve months of steady income, starting from an empty company.
	year := p.breakdownMonths(repeat(3000, 12), nil, yearMonth{2026, time.July},
		companyStock{Known: true})

	if year.MonthsHeldForTarget == 0 {
		t.Fatal("no month was held for the target, so nothing was saved")
	}
	if year.MonthsHeldForTarget == 12 {
		t.Fatal("every month was held — the target was never reached and this proves nothing")
	}
	if year.CompanyClosingCents != 400000 {
		t.Errorf("closing = %d, want exactly the 4,000 target — the year should end sitting on it", year.CompanyClosingCents)
	}
	if year.MonthsAtMinimum != year.MonthsHeldForTarget {
		t.Errorf("MonthsAtMinimum = %d, MonthsHeldForTarget = %d — a held month is a minimum month",
			year.MonthsAtMinimum, year.MonthsHeldForTarget)
	}
}

// TestTheTargetSaysWhyOnThePage: the gross salary in a held month matches
// neither affordability nor anything in config.json, so the page has to name
// the cause or the figure is unexplainable.
func TestTheTargetSaysWhyOnThePage(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Private","kind":"private","balance":2000,"as_of":"2026-07-31"},
		{"name":"Company","kind":"company","balance":500,"as_of":"2026-07-31"}
	]}`)
	trk.Personal = bulgariaBands()
	target, err := ParseTargetPlan([]TargetEntry{{From: "2026-01", Amount: f64(9000)}})
	if err != nil {
		t.Fatal(err)
	}
	trk.Personal.Target = target

	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if !f.FundingPersonal.HeldForTarget {
		t.Fatal("a company at 500 against a 9,000 target was not held at the minimum")
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()
	for _, want := range []string{"Held at the statutory minimum", "saving towards", "9,000", "target 9,000"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page never says %q", want)
		}
	}
	// The gross row itself names the cause. "minimum by choice" would be the
	// wrong reason — nothing in config.json asked for a minimum here.
	if !strings.Contains(body, "minimum until the company reaches 9,000") {
		t.Error("the gross salary row does not say why it is at the minimum")
	}
	if strings.Contains(body, "minimum by choice") {
		t.Error("a target-held month is labelled as a chosen minimum, which is not what was chosen")
	}
}
