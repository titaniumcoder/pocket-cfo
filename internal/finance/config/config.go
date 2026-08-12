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

	AnnualVacationDays *int `json:"annualVacationDays"`

	// Legislation is every government-set figure the salary cascade uses, and
	// the only place any of them is configured — there is no such thing as an
	// undated tax rate:
	//
	//   "legislation": [
	//     { "from": "2026-01", "socialEmployerRate": 0.1892,
	//       "socialEmployeeRate": 0.1378, "socialMaxInsurableMonthly": 2112,
	//       "incomeTaxRate": 0.10 },
	//     { "from": "2026-07", "minimumWage": 1077 },
	//     { "from": "2027-01", "minimumWage": 1150,
	//       "socialMaxInsurableMonthly": 2400, "socialEmployerRate": 0.199 }
	//   ]
	//
	// An entry states what changed, not what stayed: every figure is optional
	// and carries forward. Rates and the ceiling also apply backwards from the
	// earliest entry, since a month has to have a tax rate; the minimum wage
	// does not, because employment has a start date. Omitted entirely, the
	// defaults in defaultLegislation apply.
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

		AnnualVacationDays: intOr(fc.AnnualVacationDays, 25),
		// Already validated by LoadFileConfig, which is the only way to get a
		// FileConfig with entries in it.
		Legislation: legislationOrDefault(fc.Legislation),
	}
}

// defaultLegislation is what applies when config.json says nothing: Bulgaria's
// figures as of 2026, dated so /info shows them like any other period rather
// than leaving a reader to guess where the numbers came from. No minimum wage,
// since nobody is employed until the file says they are.
func defaultLegislation() tracker.Legislation {
	return tracker.Legislation{{
		From:          tracker.FromTheStart,
		EmployerRate:  ptr(0.1892),
		EmployeeRate:  ptr(0.1378),
		MaxInsurable:  ptr(2112.0),
		IncomeTaxRate: ptr(0.10),
	}}
}

func ptr(v float64) *float64 { return &v }

func legislationOrDefault(entries []tracker.LegislationEntry) tracker.Legislation {
	periods, err := tracker.ParseLegislation(entries)
	if err != nil || len(periods) == 0 {
		return defaultLegislation()
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
