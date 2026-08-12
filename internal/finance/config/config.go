// Package config loads the finance tracker's own settings: config.json for
// non-secret tunables, plus a few finance-specific env vars. Deliberately no
// JSON Schema or codegen here — a hand-written struct is simpler at this size,
// the one exception to the rule in AGENTS.md. Anything shared with invoicing
// stays in cmd/pocketcfo's config.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// FileConfig is config.json's shape. Every field is optional; a nil pointer, or
// no file at all, falls back to Load's defaults. The tags are camelCase unlike
// the app's snake_case wire formats: config.json is hand-edited in the private
// data repo, and its existing casing stays as-is.
type FileConfig struct {
	HoursPerDay        *float64 `json:"hoursPerDay"`
	TogglProjectIDs    []int    `json:"togglProjectIds"`
	HolidayCountry     *string  `json:"holidayCountry"`
	HolidaySubdivision *string  `json:"holidaySubdivision"`

	// HourlyRateCents/Currency are the primary source of the Expected-work
	// projection rate (see internal/finance/tracker.Tracker.RateCents) —
	// a plain config value, not fetched from Toggl.
	HourlyRateCents *int    `json:"hourlyRateCents"`
	Currency        *string `json:"currency"`

	SocialEmployerRate        *float64 `json:"socialEmployerRate"`
	SocialEmployeeRate        *float64 `json:"socialEmployeeRate"`
	SocialMaxInsurableMonthly *float64 `json:"socialMaxInsurableMonthly"`
	IncomeTaxRate             *float64 `json:"incomeTaxRate"`
	AnnualVacationDays        *int     `json:"annualVacationDays"`

	// Legislation is every dated change to the payroll figures above, plus
	// the minimum wage, which has no undated form:
	//
	//   "legislation": [
	//     { "from": "2026-07", "minimumWage": 1077 },
	//     { "from": "2027-01", "minimumWage": 1150,
	//       "socialMaxInsurableMonthly": 2400, "socialEmployerRate": 0.199 }
	//   ]
	//
	// The flat keys above are the baseline, in force until a period says
	// otherwise. An entry states what changed, not what stayed. The earliest
	// minimumWage is also when employment began: before it there is no floor,
	// because there was no job.
	Legislation []tracker.LegislationEntry `json:"legislation"`
}

// LoadFileConfig reads config.json from path. A missing file is fine and
// returns a zero-value FileConfig (every setting falls back to its
// default); a malformed file is a fail-fast error.
func LoadFileConfig(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileConfig{}, nil
		}
		return FileConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var fc FileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	// Validated here, where the error can be returned, rather than in Load,
	// which cannot fail. These are legal obligations: a typo in a date
	// silently disabling one is the failure the setting exists to prevent, so
	// it is the one part of this file that does not degrade quietly.
	if _, err := tracker.ParseLegislation(fc.Legislation); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return fc, nil
}

// Config is the finance tracker's fully-resolved settings.
type Config struct {
	// TogglToken/TogglWorkspace are read straight from the environment
	// (credentials, not config.json). Both empty means the Toggl
	// tracked-hours layer is disabled entirely — see BuildToggl in
	// cmd/pocketcfo, which leaves Tracker.Toggl nil in that case; this is
	// deliberately not a hard requirement, unlike money-fun's original
	// fail-fast design, per PocketCFO's "Toggl is optional, config-toggled"
	// plan.
	TogglToken     string
	TogglWorkspace string
	TogglProjects  string // comma-separated, see joinIDs
	Country        string // optional, e.g. "AT"; empty falls back to "AT" — see tracker.Holidays.countryOrDefault
	Subdivision    string

	HoursPerDay     float64
	HourlyRateCents int
	Currency        string

	EmployerRate        float64
	EmployeeRate        float64
	MaxInsurableMonthly float64
	IncomeTaxRate       float64
	AnnualVacationDays  int
	Legislation         tracker.Legislation
}

// Load merges fc (already loaded via LoadFileConfig) with the
// finance-specific environment variables into a fully-resolved Config.
// Unlike cmd/pocketcfo's main config.go, nothing here is fail-fast: every
// setting has a workable default or degrades to "layer disabled" (Toggl),
// since the finance tracker should stay usable even with minimal setup.
func Load(fc FileConfig) Config {
	return Config{
		TogglToken:     os.Getenv("TOGGL_API_TOKEN"),
		TogglWorkspace: os.Getenv("TOGGL_WORKSPACE_ID"),
		TogglProjects:  joinIDs(fc.TogglProjectIDs),
		Country:        strOr(fc.HolidayCountry, "AT"),
		Subdivision:    strOr(fc.HolidaySubdivision, ""),

		HoursPerDay:     floatOr(fc.HoursPerDay, 8),
		HourlyRateCents: intOr(fc.HourlyRateCents, 0),
		Currency:        strOr(fc.Currency, "EUR"),

		EmployerRate:        floatOr(fc.SocialEmployerRate, 0.1892),
		EmployeeRate:        floatOr(fc.SocialEmployeeRate, 0.1378),
		MaxInsurableMonthly: floatOr(fc.SocialMaxInsurableMonthly, 2112),
		IncomeTaxRate:       floatOr(fc.IncomeTaxRate, 0.10),
		AnnualVacationDays:  intOr(fc.AnnualVacationDays, 25),
		// Already validated by LoadFileConfig, which is the only way to get a
		// FileConfig with entries in it.
		Legislation: mustParseLegislation(fc.Legislation),
	}
}

func mustParseLegislation(entries []tracker.LegislationEntry) tracker.Legislation {
	periods, err := tracker.ParseLegislation(entries)
	if err != nil {
		return nil
	}
	return periods
}

func floatOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func strOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// joinIDs renders project IDs as the comma-separated string tracker.Toggl
// already expects, so FileConfig.TogglProjectIDs (a proper JSON array) can
// still feed the existing string-based filter unchanged.
func joinIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}
