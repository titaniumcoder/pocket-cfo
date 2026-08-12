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
	//     { "from": "2026-01",
	//       "contributions": {
	//         "employer": { "bands": [ {"from": 0, "rate": 0.1892}, {"from": 2112, "rate": 0} ] },
	//         "employee": { "bands": [ {"from": 0, "rate": 0.1378}, {"from": 2112, "rate": 0} ] }
	//       },
	//       "incomeTax": { "bands": [ {"from": 0, "rate": 0.10} ] } },
	//     { "from": "2026-07", "minimumWage": 1077 },
	//     { "from": "2027-01", "minimumWage": 1150,
	//       "contributions": {
	//         "employer": { "bands": [ {"from": 0, "rate": 0.199}, {"from": 2400, "rate": 0} ] }
	//       } }
	//   ]
	//
	// Bands are marginal: a rate applies only to the slice of the base inside
	// its own band, which is why a ceiling is a band with a rate of zero rather
	// than a field of its own. Each party has its own schedule, because
	// employer and employee thresholds genuinely differ.
	//
	// An entry states what changed, not what stayed: every figure is optional
	// and stays in force until a later entry changes it, though a band list is
	// one indivisible statement and replaces its predecessor whole.
	//
	// Nothing applies before the earliest entry, and there are no built-in
	// figures to fall back on — a figure no entry has stated is zero. Invented
	// defaults are how a page ends up reporting a rate that is nowhere in this
	// file, which for a legal obligation is worse than reporting none: a zero
	// is visible as an omission, a plausible wrong rate is not.
	Legislation []tracker.LegislationEntry `json:"legislation"`

	// legislation is Legislation parsed, kept from the one validating parse in
	// LoadFileConfig so Load cannot parse it a second time and disagree.
	// Unexported, so a hand-built FileConfig carries no legislation — which is
	// now an ordinary state, not a trigger for anything.
	legislation tracker.Legislation

	// Salary is which months pay a full salary, only the statutory minimum, or
	// none at all. Legislation says what a salary costs; this says whether one
	// was drawn, which is a decision rather than a law.
	//
	// Each entry is a dated stretch: from is required, to is inclusive and
	// optional, and an entry without one runs until the next begins — the same
	// carry-forward the legislation block uses, so the two read alike. Any
	// month no entry covers pays a full salary, so an absent block leaves
	// every month exactly as it was.
	//
	//	"salary": [
	//	  { "from": "2026-04", "to": "2026-06", "mode": "minimum" },
	//	  { "from": "2026-09", "mode": "none" }
	//	]
	//
	// minimum pays exactly the statutory minimum however much was affordable,
	// leaving the difference in the company; none pays nothing at all, so
	// nothing is contributed or taxed either.
	Salary []tracker.SalaryEntry `json:"salary"`

	salary tracker.SalaryPlan
}

// LoadFileConfig reads config.json from path. A missing file is fine and
// returns a zero-value FileConfig — every tunable falls back to its default and
// no legislation is in force, so nothing is contributed or taxed; a malformed
// file is a fail-fast error.
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
	if fc.legislation, err = tracker.ParseLegislation(fc.Legislation); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if fc.salary, err = tracker.ParseSalaryPlan(fc.Salary); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	// A month set to the minimum with no minimum wage in force pays nothing,
	// which is a different decision from the one that was written down — and
	// one the page would report as a salary of zero without ever saying why.
	// The two blocks only meet here, so this is the only place that can catch
	// it.
	if err := tracker.ValidateSalaryAgainstLegislation(fc.salary, fc.legislation); err != nil {
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

	AnnualVacationDays int
	Legislation        tracker.Legislation
	Salary             tracker.SalaryPlan
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
		// Parsed and validated by LoadFileConfig, which is the only thing that
		// reads the entries. Nothing is substituted when a file states no
		// legislation: it then charges nothing, which is what it says.
		Legislation: fc.legislation,
		Salary:      fc.salary,
	}
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
