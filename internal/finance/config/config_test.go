package config

import (
	"os"
	"path/filepath"
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
	// The rates live in one default period rather than as loose fields, so a
	// file that says nothing still has a complete, visible set of figures
	// rather than four zeroes.
	if len(cfg.Legislation) != 1 {
		t.Fatalf("Legislation = %v, want one default period", cfg.Legislation)
	}
	def := cfg.Legislation[0]
	if def.EmployerRate == nil || *def.EmployerRate != 0.1892 || def.EmployeeRate == nil || *def.EmployeeRate != 0.1378 {
		t.Errorf("default social rates = %+v, want 0.1892/0.1378", def)
	}
	if def.MinimumWage != nil {
		t.Errorf("a default minimum wage of %v was invented; nobody is employed until the file says so", *def.MinimumWage)
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

// TestLoadFileConfigRefusesMalformedLegislation: every other setting here
// degrades quietly to a default, and this one must not. These are legal
// obligations, so a typo in a date silently disabling one is the single
// failure the setting exists to prevent.
func TestLoadFileConfigRefusesMalformedLegislation(t *testing.T) {
	bad := map[string]string{
		"a date that is not one":    `{"legislation":[{"from":"July 2026","minimumWage":1077}]}`,
		"no date at all":            `{"legislation":[{"minimumWage":1077}]}`,
		"a negative amount":         `{"legislation":[{"from":"2026-07","minimumWage":-5}]}`,
		"two entries for a month":   `{"legislation":[{"from":"2026-07","minimumWage":1077},{"from":"2026-07-20","minimumWage":1100}]}`,
		"an entry changing nothing": `{"legislation":[{"from":"2026-07"}]}`,
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
		{"from":"2027-01","minimumWage":1150,"socialEmployerRate":0.199},
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
	if got[0].EmployerRate != nil {
		t.Errorf("July's entry invented an employer rate: %v", *got[0].EmployerRate)
	}
	if got[1].EmployerRate == nil || *got[1].EmployerRate != 0.199 {
		t.Errorf("January's rate did not survive: %+v", got[1])
	}
}

// TestNoLegislationFallsBackToTheDefaults: a file that says nothing still has
// a complete set of figures, dated so /info shows where they came from. Zero
// rates would mean a salary with no deductions at all, which is not a subtle
// kind of wrong.
func TestNoLegislationFallsBackToTheDefaults(t *testing.T) {
	got := Load(FileConfig{}).Legislation
	if len(got) != 1 || got[0].From != tracker.FromTheStart {
		t.Fatalf("Legislation = %v, want one undated default period", got)
	}
	if got[0].IncomeTaxRate == nil || *got[0].IncomeTaxRate != 0.10 {
		t.Errorf("default tax rate = %+v", got[0])
	}
}
