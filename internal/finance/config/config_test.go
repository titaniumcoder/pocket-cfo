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
	t.Setenv("API_PASSWORD", "pw")
	cfg := Load(FileConfig{})
	if cfg.TogglToken != "tok" || cfg.TogglWorkspace != "ws" {
		t.Errorf("Toggl creds = %q/%q, want tok/ws", cfg.TogglToken, cfg.TogglWorkspace)
	}
	if cfg.APIPassword != "pw" {
		t.Errorf("APIPassword = %q, want pw", cfg.APIPassword)
	}
}
