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

func newAccountsFS(t *testing.T, body string) *tracker.Accounts {
	t.Helper()
	return &tracker.Accounts{FS: fstest.MapFS{"accounts.json": &fstest.MapFile{Data: []byte(body)}}}
}
