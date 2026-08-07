// Package config loads the finance tracker's own settings: config.json for
// non-secret tunables (rates, filters, defaults — no JSON Schema here, same
// deliberate exception invoicer's own config.json-equivalent doesn't have
// either: this is small enough that a hand-written struct read with plain
// encoding/json is simpler than adding codegen for it), plus a handful of
// finance-specific environment variables Load reads directly. Everything
// shared with the invoicing side (GitHub OAuth, session secret, users.json)
// stays in cmd/pocketcfo's own config — this package only covers what's
// specific to the finance part.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// FileConfig is config.json's shape. Every field is optional; a nil pointer
// (or a missing config.json entirely) falls back to the default baked into
// Load. JSON tags are deliberately camelCase, unlike the rest of the app's
// own wire formats (session/OTP/the net-income API, all snake_case) —
// config.json is a hand-edited file in the private companion data repo, so
// its existing key casing stays as-is rather than being forced to match.
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

	// APIPassword gates GET /api/net-income/... — empty disables the API
	// entirely (every request is rejected, same as an unset password never
	// matching anything).
	APIPassword string
}

// Load merges fc (already loaded via LoadFileConfig) with the
// finance-specific environment variables into a fully-resolved Config.
// Unlike cmd/pocketcfo's main config.go, nothing here is fail-fast: every
// setting has a workable default or degrades to "layer disabled" (Toggl) /
// "endpoint disabled" (the JSON API), since the finance tracker should stay
// usable even with minimal setup.
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

		APIPassword: os.Getenv("API_PASSWORD"),
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
