package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func TestLoadFileConfig_MissingFileIsFine(t *testing.T) {
	fc, err := LoadFileConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if fc.HoursPerDay != nil {
		t.Errorf("HoursPerDay = %v, want nil for a missing file", fc.HoursPerDay)
	}
}

func TestLoadFileConfig_Malformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFileConfig(path); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestLoadFileConfig_Parses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"hoursPerDay": 6, "togglProjectIds": [1, 2, 3], "hourlyRateCents": 7500, "currency": "USD"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if fc.HoursPerDay == nil || *fc.HoursPerDay != 6 {
		t.Errorf("HoursPerDay = %v, want 6", fc.HoursPerDay)
	}
	if len(fc.TogglProjectIDs) != 3 {
		t.Errorf("TogglProjectIDs = %v, want [1 2 3]", fc.TogglProjectIDs)
	}
	if fc.HourlyRateCents == nil || *fc.HourlyRateCents != 7500 {
		t.Errorf("HourlyRateCents = %v, want 7500", fc.HourlyRateCents)
	}
	if fc.Currency == nil || *fc.Currency != "USD" {
		t.Errorf("Currency = %v, want USD", fc.Currency)
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg := Load(FileConfig{})
	if cfg.HoursPerDay != 8 {
		t.Errorf("HoursPerDay = %v, want default 8", cfg.HoursPerDay)
	}
	if cfg.Currency != "EUR" {
		t.Errorf("Currency = %q, want default EUR", cfg.Currency)
	}
	if cfg.HourlyRateCents != 0 {
		t.Errorf("HourlyRateCents = %v, want default 0 (unset)", cfg.HourlyRateCents)
	}
	if cfg.AnnualVacationDays != 25 {
		t.Errorf("AnnualVacationDays = %d, want default 25", cfg.AnnualVacationDays)
	}
	if cfg.TogglToken != "" || cfg.TogglWorkspace != "" {
		t.Error("TogglToken/TogglWorkspace should be empty when the env vars aren't set")
	}
	if cfg.Country != "AT" {
		t.Errorf("Country = %q, want default AT (matches the previously-hardcoded value)", cfg.Country)
	}
}

func TestLoad_HolidayCountryOverride(t *testing.T) {
	country := "BG"
	cfg := Load(FileConfig{HolidayCountry: &country})
	if cfg.Country != "BG" {
		t.Errorf("Country = %q, want BG", cfg.Country)
	}
}

func TestLoad_OverridesFromFileConfig(t *testing.T) {
	rate := 6000
	currency := "GBP"
	days := 30
	cfg := Load(FileConfig{
		HourlyRateCents:    &rate,
		Currency:           &currency,
		AnnualVacationDays: &days,
		TogglProjectIDs:    []int{10, 20},
	})
	if cfg.HourlyRateCents != 6000 {
		t.Errorf("HourlyRateCents = %d, want 6000", cfg.HourlyRateCents)
	}
	if cfg.Currency != "GBP" {
		t.Errorf("Currency = %q, want GBP", cfg.Currency)
	}
	if cfg.AnnualVacationDays != 30 {
		t.Errorf("AnnualVacationDays = %d, want 30", cfg.AnnualVacationDays)
	}
	if cfg.TogglProjects != "10,20" {
		t.Errorf("TogglProjects = %q, want 10,20", cfg.TogglProjects)
	}
}

func TestLoad_TogglFromEnv(t *testing.T) {
	t.Setenv("TOGGL_API_TOKEN", "tok")
	t.Setenv("TOGGL_WORKSPACE_ID", "ws")
	cfg := Load(FileConfig{})
	if cfg.TogglToken != "tok" || cfg.TogglWorkspace != "ws" {
		t.Errorf("Toggl creds = %q/%q, want tok/ws", cfg.TogglToken, cfg.TogglWorkspace)
	}
}

// TestLoadFileConfigRefusesMalformedPayrollRules: every other setting here
// degrades quietly to a default, and these must not. They are legal
// obligations, so a typo in a date silently disabling one is the single
// failure the settings exist to prevent.
func TestLoadFileConfigRefusesMalformedPayrollRules(t *testing.T) {
	bad := map[string]string{
		"a date that is not one":    `{"legislation":[{"from":"July 2026","minimumWage":1077}]}`,
		"no date at all":            `{"legislation":[{"minimumWage":1077}]}`,
		"a negative amount":         `{"legislation":[{"from":"2026-07","minimumWage":-5}]}`,
		"two entries for a month":   `{"legislation":[{"from":"2026-07","minimumWage":1077},{"from":"2026-07-20","minimumWage":1100}]}`,
		"an entry changing nothing": `{"legislation":[{"from":"2026-07"}]}`,
		"bands not starting at 0":   `{"legislation":[{"from":"2026-07","contributions":{"employer":{"bands":[{"from":100,"rate":0.1}]}}}]}`,
		"bands out of order":        `{"legislation":[{"from":"2026-07","contributions":{"employer":{"bands":[{"from":0,"rate":0.1},{"from":0,"rate":0.2}]}}}]}`,
		// A file still on the v0.15.0 keys must not start. The fallback to
		// built-in defaults means a half-migrated one reports plausible wrong
		// numbers, which is worse than refusing.
		"the retired flat rates": `{"legislation":[{"from":"2026-07","socialEmployerRate":0.1892,"socialMaxInsurableMonthly":2112}]}`,
		"the retired tax rate":   `{"legislation":[{"from":"2026-07","incomeTaxRate":0.10}]}`,
		// A fixed salary is the one figure the app pays without checking it
		// can, so a typo in it is not caught by anything downstream.
		"a fixed salary with no amount":    `{"salary":[{"from":"2026-04","mode":"fixed"}]}`,
		"an amount on a mode that ignores": `{"salary":[{"from":"2026-04","mode":"full","amount":2500}]}`,
		"a fixed salary below the wage":    `{"legislation":[{"from":"2026-01","minimumWage":1077}],"salary":[{"from":"2026-04","to":"2026-05","mode":"fixed","amount":800}]}`,
		// A target holds months at the statutory minimum, so it needs one to
		// hold them at; without it the month silently pays nothing.
		"a target with no amount":       `{"targetBalance":[{"from":"2026-04"}]}`,
		"overlapping targets":           `{"targetBalance":[{"from":"2026-04","to":"2026-08","amount":10000},{"from":"2026-06","amount":20000}]}`,
		"a target with no minimum wage": `{"targetBalance":[{"from":"2026-04","to":"2026-05","amount":20000}]}`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFileConfig(path); err == nil {
				t.Error("loaded without complaint; the floor would silently not apply")
			}
		})
	}
}

// TestLoadResolvesLegislation closes the loop: a valid block reaches Config in
// date order, so the tracker gets periods rather than strings.
func TestLoadResolvesLegislation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"legislation":[
		{"from":"2027-01","minimumWage":1150,
		 "contributions":{"employer":{"bands":[{"from":0,"rate":0.199},{"from":2400,"rate":0}]}}},
		{"from":"2026-07","minimumWage":1077}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := Load(fc).Legislation
	if len(got) != 2 {
		t.Fatalf("resolved %d periods, want 2", len(got))
	}
	if got[0].MinimumWage == nil || *got[0].MinimumWage != 1077 {
		t.Errorf("periods = %v, want the earlier month first", got)
	}
	// A figure an entry never mentions stays nil rather than becoming zero,
	// which is what lets it carry forward instead of resetting.
	if got[0].Employer != nil {
		t.Errorf("July's entry invented an employer schedule: %+v", got[0].Employer)
	}
	want := tracker.Bands{{From: 0, Rate: 0.199}, {From: 2400, Rate: 0}}
	if got[1].Employer == nil || !reflect.DeepEqual(got[1].Employer.Bands, want) {
		t.Errorf("January's schedule did not survive: %+v", got[1])
	}
}

// TestNoLegislationIsNoLegislation: a file that states none gets none. There
// are no built-in figures to fall back on — a rate nobody wrote down reported
// as if they had is worse than a zero, which at least reads as the omission it
// is (the page says so; see tracker.PersonalView.NoLegislation).
func TestNoLegislationIsNoLegislation(t *testing.T) {
	if got := Load(FileConfig{}).Legislation; len(got) != 0 {
		t.Fatalf("Legislation = %v, want none — nothing said, nothing charged", got)
	}
}

// TestALegislationWithoutRatesStillLoads: an entry has to state something, but
// nothing says it must state a rate. A file that only ever names a minimum wage
// charges no contributions and no tax, and that is an answer rather than a
// mistake to reject.
func TestALegislationWithoutRatesStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"legislation":[{"from":"2026-07","minimumWage":1077}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := Load(fc).Legislation
	if len(got) != 1 || got[0].Employer != nil || got[0].Employee != nil || got[0].IncomeTax != nil {
		t.Errorf("Legislation = %+v, want the one minimum-wage entry and no invented rates", got)
	}
}
