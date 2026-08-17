package api

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func dividendService(t *testing.T) *Service {
	t.Helper()
	legislation, err := tracker.ParseLegislation([]tracker.LegislationEntry{{
		From:             "2026-01",
		CompanyProfitTax: &tracker.TaxEntry{Bands: []tracker.BandEntry{{From: 0, Rate: rate(0.10)}}},
		DividendTax:      &tracker.TaxEntry{Bands: []tracker.BandEntry{{From: 0, Rate: rate(0.05)}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		Budget: &tracker.Budget{FS: fstest.MapFS{"budget.json": &fstest.MapFile{Data: []byte(`{"groups":[
			{"name":"Housing","kind":"private","categories":[{"id":"` + idRent + `","name":"Rent","amount":900}]}
		],"dividends":[{"date":"2026-09-30","amount":10000,"note":"2025 profit distributed"}]}`)}}},
		Personal: tracker.PersonalParams{Legislation: legislation},
		Now:      func() time.Time { return time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC) },
	}
}

func rate(v float64) *float64 { return &v }

// TestGetBudgetReportsTheDividendWithItsTaxesWorkedOut: an agent that has to
// recompute the taxes will resolve the rates to the wrong month sooner or
// later, so the arithmetic travels with the plan.
func TestGetBudgetReportsTheDividendWithItsTaxesWorkedOut(t *testing.T) {
	s := dividendService(t)

	mb, err := s.BudgetForMonth(context.Background(), "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if len(mb.Dividends) != 1 {
		t.Fatalf("got %d dividends, want the one planned for September: %+v", len(mb.Dividends), mb.Dividends)
	}
	d := mb.Dividends[0]
	if d.GrossCents != 1000000 || d.CompanyProfitTaxCents != 100000 || d.DividendTaxCents != 50000 {
		t.Errorf("dividend = %+v, want 10000 gross with 10%% and 5%% worked out", d)
	}
	if d.NetToOwnerCents != 950000 {
		t.Errorf("net to owner = %d, want the gross less its dividend tax", d.NetToOwnerCents)
	}
	if d.CostToCompanyCents != 1100000 {
		t.Errorf("cost to company = %d, want the gross plus its profit tax", d.CostToCompanyCents)
	}

	// A month without one says nothing rather than reporting a zero.
	quiet, err := s.BudgetForMonth(context.Background(), "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet.Dividends) != 0 {
		t.Errorf("August reports %+v, want no distribution at all", quiet.Dividends)
	}
}

// TestGetFinanceConfigNamesTheDividendRates, and says it cannot be written
// here — otherwise the first thing an agent does with a rate it disagrees with
// is look for the tool to change it.
func TestGetFinanceConfigNamesTheDividendRates(t *testing.T) {
	cfg, err := dividendService(t).FinanceConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Legislation) != 1 {
		t.Fatalf("got %d periods, want the one configured", len(cfg.Legislation))
	}
	if cfg.Legislation[0].From != "2026-01" {
		t.Errorf("from = %q, want it spelled as config.json spells it", cfg.Legislation[0].From)
	}
	for _, want := range []string{"company profit tax 10%", "dividend tax 5%"} {
		if !strings.Contains(cfg.Legislation[0].InForce, want) {
			t.Errorf("the period %q never says %q", cfg.Legislation[0].InForce, want)
		}
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
