package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

type FileConfig struct {
	HoursPerDay        *float64 `json:"hoursPerDay"`
	TogglProjectIDs    []int    `json:"togglProjectIds"`
	HolidayCountry     *string  `json:"holidayCountry"`
	HolidaySubdivision *string  `json:"holidaySubdivision"`

	HourlyRateCents *int    `json:"hourlyRateCents"`
	Currency        *string `json:"currency"`

	AnnualVacationDays *int `json:"annualVacationDays"`

	Legislation []tracker.LegislationEntry `json:"legislation"`

	legislation tracker.Legislation

	Salary []tracker.SalaryEntry `json:"salary"`

	salary tracker.SalaryPlan

	TargetBalance []tracker.TargetEntry `json:"targetBalance"`

	targetBalance tracker.TargetPlan
	targetIdle    []string

	StartMonth *string `json:"startMonth"`

	startMonth time.Time
}

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
	if fc.legislation, err = tracker.ParseLegislation(fc.Legislation); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if fc.salary, err = tracker.ParseSalaryPlan(fc.Salary); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := tracker.ValidateSalaryAgainstLegislation(fc.salary, fc.legislation); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if fc.targetBalance, err = tracker.ParseTargetPlan(fc.TargetBalance); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := tracker.RequireMinimumWageForTargets(fc.targetBalance, fc.legislation); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	fc.targetIdle = tracker.ValidateTargetAgainstSalary(fc.targetBalance, fc.salary)
	if fc.startMonth, err = tracker.ParseStartMonth(strOr(fc.StartMonth, "")); err != nil {
		return FileConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return fc, nil
}

type Config struct {
	TogglToken     string
	TogglWorkspace string
	TogglProjects  string
	Country        string
	Subdivision    string

	HoursPerDay     float64
	HourlyRateCents int
	Currency        string

	AnnualVacationDays int
	Legislation        tracker.Legislation
	Salary             tracker.SalaryPlan
	TargetBalance      tracker.TargetPlan
	TargetIdleMonths   []string

	StartMonth time.Time
}

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
		Legislation:        fc.legislation,
		Salary:             fc.salary,
		TargetBalance:      fc.targetBalance,
		TargetIdleMonths:   fc.targetIdle,
		StartMonth:         fc.startMonth,
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

func joinIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}
