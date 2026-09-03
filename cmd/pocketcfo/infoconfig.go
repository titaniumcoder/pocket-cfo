package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/buildinfo"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
)

type configRow struct {
	Name   string
	Value  string
	Secret bool
}

type configGroup struct {
	Name string
	Rows []configRow
}

const unsetLabel = "(unset)"

func maskSecret(v string) string {
	if v == "" {
		return unsetLabel
	}
	r := []rune(v)
	const shown = 5
	if len(r) <= shown*2 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:3]) + strings.Repeat("*", len(r)-shown) + string(r[len(r)-2:])
}

func orUnset(v string) string {
	if v == "" {
		return unsetLabel
	}
	return v
}

func (s *server) configGroups() []configGroup {
	c := s.cfg
	f := c.finance

	return []configGroup{
		{Name: "Application", Rows: []configRow{
			{Name: "Version", Value: buildinfo.Version},
			{Name: "DATA_UPDATED_AT", Value: orUnset(buildinfo.Data.UpdatedAt)},
			{Name: "DATA_COMMIT", Value: orUnset(buildinfo.Data.Commit)},
			{Name: "ENV", Value: orUnset(c.env)},
			{Name: "PORT", Value: orUnset(c.port)},
			{Name: "PUBLIC_BASE_URL", Value: orUnset(c.baseURL)},
			{Name: "GITHUB_REPO", Value: orUnset(c.repo)},
		}},
		{Name: "Paths", Rows: []configRow{
			{Name: "DATA_DIR", Value: dataDir},
			{Name: "BUILD_DIR", Value: buildDir},
			{Name: "TEMPLATES_DIR", Value: templatesDir},
			{Name: "STATIC_DIR", Value: staticDir},
			{Name: "CATALOG_DIR", Value: getenv("CATALOG_DIR", "catalog")},
			{Name: "CONFIG_FILE", Value: getenv("CONFIG_FILE", "config.json")},
			{Name: "users.json", Value: usersFile},
		}},
		{Name: "Credentials", Rows: []configRow{
			{Name: "GITHUB_OAUTH_CLIENT_ID", Value: maskSecret(c.clientID), Secret: true},
			{Name: "GITHUB_OAUTH_CLIENT_SECRET", Value: maskSecret(c.clientSecret), Secret: true},
			{Name: "SESSION_SECRET", Value: maskSecret(c.sessionSecret), Secret: true},
			{Name: "CLIENT_LINK_SECRET", Value: maskSecret(c.clientLinkSecret), Secret: true},
			{Name: "OTP_LINK_SECRET", Value: maskSecret(c.otpLinkSecret), Secret: true},
			{Name: "API2PDF_KEY", Value: maskSecret(c.api2pdfKey), Secret: true},
		}},
		{Name: "Toggl", Rows: []configRow{
			{Name: "TOGGL_MODE", Value: togglModeSummary(f.TogglMode, c.togglMode)},
			{Name: "TOGGL_API_TOKEN", Value: maskSecret(f.TogglToken), Secret: true},
			{Name: "TOGGL_WORKSPACE_ID", Value: orUnset(f.TogglWorkspace)},
			{Name: "togglProjectIds", Value: orUnset(f.TogglProjects)},
			{Name: "TOGGL2_API_KEY", Value: maskSecret(f.Toggl2Key), Secret: true},
			{Name: "TOGGL2_ORGANIZATION_ID", Value: orUnset(f.Toggl2Organization)},
			{Name: "TOGGL2_WORKSPACE_ID", Value: orUnset(f.Toggl2Workspace)},
			{Name: "TOGGL2_API_KEY_EXPIRES_AT", Value: keyExpirySummary(f.Toggl2KeyExpiresAt)},
			{Name: "toggl2ProjectIds", Value: orUnset(f.Toggl2Projects)},
			{Name: "TOGGL_REFRESH_INTERVAL", Value: togglRefreshInterval().String()},
		}},
		{Name: "Hermes API", Rows: []configRow{
			{Name: "HERMES_API_TOKEN", Value: maskSecret(c.hermesAPIToken), Secret: true},
			{Name: "GITHUB_DATA_TOKEN", Value: maskSecret(c.githubDataToken), Secret: true},
			{Name: "API routes", Value: enabledIf(c.hermesAPIToken != "")},
			{Name: "Writes", Value: enabledIf(c.githubDataToken != "")},
			{Name: "Write target", Value: orUnset(c.repo)},
			{Name: "GITHUB_API_URL", Value: orUnset(c.githubAPIURL)},
		}},
		{Name: "Email (Amazon SES)", Rows: []configRow{
			{Name: "AWS_REGION", Value: orUnset(c.sesRegion)},
			{Name: "SES_FROM_EMAIL", Value: orUnset(c.sesFromEmail)},
			{Name: "AWS_ACCESS_KEY_ID", Value: maskSecret(os.Getenv("AWS_ACCESS_KEY_ID")), Secret: true},
			{Name: "AWS_SECRET_ACCESS_KEY", Value: maskSecret(os.Getenv("AWS_SECRET_ACCESS_KEY")), Secret: true},
		}},
		{Name: "Finance (config.json)", Rows: []configRow{
			{Name: "holidayCountry", Value: orUnset(f.Country)},
			{Name: "holidaySubdivision", Value: orUnset(f.Subdivision)},
			{Name: "hoursPerDay", Value: strconv.FormatFloat(f.HoursPerDay, 'f', -1, 64)},
			{Name: "hourlyRateCents", Value: hourlyRateSummary(f.HourlyRateCents, f.Currency)},
			{Name: "currency", Value: orUnset(f.Currency)},
			{Name: "annualVacationDays", Value: strconv.Itoa(f.AnnualVacationDays)},
			{Name: "legislation / salary / targetBalance / startMonth", Value: "see the rules timeline below"},
		}},
	}
}

func hourlyRateSummary(cents int, currency string) string {
	return strconv.Itoa(cents) + " (" + render.FormatAmount(int64(cents)) + " " + currency + " an hour)"
}

func togglModeSummary(raw, resolved string) string {
	label := string(togglModeLabel(resolved))
	if raw == "" {
		return "unset — " + label
	}
	return raw + " — " + label
}

func keyExpirySummary(t time.Time) string {
	if t.IsZero() {
		return "unset — no advance warning, only a rejected key is reported"
	}
	return t.Format("2006-01-02")
}

func enabledIf(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}
