package config

import (
	"os"
	"path/filepath"
	"testing"
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
	if cfg.EmployerRate != 0.1892 || cfg.EmployeeRate != 0.1378 {
		t.Errorf("social rates = %v/%v, want defaults 0.1892/0.1378", cfg.EmployerRate, cfg.EmployeeRate)
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

// TestLoadFileConfigRefusesAMalformedMinimumWage: every other setting here
// degrades quietly to a default, and this one must not. A minimum wage is a
// legal obligation, so a typo in its date silently disabling the floor is the
// single failure the setting exists to prevent.
func TestLoadFileConfigRefusesAMalformedMinimumWage(t *testing.T) {
	bad := map[string]string{
		"a date that is not one":  `{"minimumWage":[{"from":"July 2026","amount":1077}]}`,
		"no date at all":          `{"minimumWage":[{"amount":1077}]}`,
		"a negative amount":       `{"minimumWage":[{"from":"2026-07","amount":-5}]}`,
		"two entries for a month": `{"minimumWage":[{"from":"2026-07","amount":1077},{"from":"2026-07-20","amount":1100}]}`,
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

// TestLoadResolvesTheMinimumWageSchedule closes the loop: a valid schedule
// reaches Config sorted, so the tracker gets periods rather than strings.
func TestLoadResolvesTheMinimumWageSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"minimumWage":[{"from":"2027-01","amount":1150},{"from":"2026-07","amount":1077}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := Load(fc).MinimumWage
	if len(got) != 2 {
		t.Fatalf("resolved %d periods, want 2", len(got))
	}
	if got[0].AmountEUR != 1077 || got[1].AmountEUR != 1150 {
		t.Errorf("periods = %v, want the earlier month first", got)
	}
}

// TestNoMinimumWageIsNoFloor: a company with no employees is the default, and
// it must stay silent rather than enforcing zero as if it were a figure.
func TestNoMinimumWageIsNoFloor(t *testing.T) {
	if got := Load(FileConfig{}).MinimumWage; len(got) != 0 {
		t.Errorf("MinimumWage = %v with nothing configured, want empty", got)
	}
}
