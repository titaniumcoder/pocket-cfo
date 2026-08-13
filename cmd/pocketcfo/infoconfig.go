package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
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
			{Name: "users.json", Value: usersFile},
		}},
		{Name: "Credentials", Rows: []configRow{
			{Name: "GITHUB_OAUTH_CLIENT_ID", Value: maskSecret(c.clientID), Secret: true},
			{Name: "GITHUB_OAUTH_CLIENT_SECRET", Value: maskSecret(c.clientSecret), Secret: true},
			{Name: "SESSION_SECRET", Value: maskSecret(c.sessionSecret), Secret: true},
			{Name: "CLIENT_LINK_SECRET", Value: maskSecret(c.clientLinkSecret), Secret: true},
			{Name: "OTP_LINK_SECRET", Value: maskSecret(c.otpLinkSecret), Secret: true},
			{Name: "API2PDF_KEY", Value: maskSecret(c.api2pdfKey), Secret: true},
			{Name: "TOGGL_API_TOKEN", Value: maskSecret(f.TogglToken), Secret: true},
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
		}},
		{Name: "Finance (config.json)", Rows: []configRow{
			{Name: "TOGGL_WORKSPACE_ID", Value: orUnset(f.TogglWorkspace)},
			{Name: "togglProjectIds", Value: orUnset(f.TogglProjects)},
			{Name: "holidayCountry", Value: orUnset(f.Country)},
			{Name: "holidaySubdivision", Value: orUnset(f.Subdivision)},
			{Name: "hoursPerDay", Value: strconv.FormatFloat(f.HoursPerDay, 'f', -1, 64)},
			{Name: "hourlyRateCents", Value: strconv.Itoa(f.HourlyRateCents)},
			{Name: "currency", Value: orUnset(f.Currency)},
			{Name: "annualVacationDays", Value: strconv.Itoa(f.AnnualVacationDays)},
			{Name: "legislation", Value: legislationSummary(f.Legislation)},
			{Name: "salary", Value: salarySummary(f.Salary)},
			{Name: "targetBalance", Value: targetBalanceSummary(f.TargetBalance, f.TargetIdleMonths)},
			{Name: "startMonth", Value: startMonthSummary(f.StartMonth)},
		}},
	}
}

func legislationSummary(periods tracker.Legislation) string {
	if len(periods) == 0 {
		return "none — nothing is contributed or taxed, and no minimum wage is enforced"
	}
	parts := make([]string, 0, len(periods))
	for _, p := range periods {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, " · ")
}

func salarySummary(plan tracker.SalaryPlan) string {
	if len(plan) == 0 {
		return "every month pays a full salary"
	}
	parts := make([]string, 0, len(plan))
	for _, p := range plan {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, " · ")
}

func targetBalanceSummary(plan tracker.TargetPlan, idle []string) string {
	if len(plan) == 0 {
		return "none — the company keeps whatever a month's salary leaves behind, with no figure it saves towards"
	}
	parts := make([]string, 0, len(plan))
	for _, p := range plan {
		parts = append(parts, p.String())
	}
	out := strings.Join(parts, " · ")
	if len(idle) > 0 {
		out += " — idle in " + strings.Join(idle, ", ") + ", because a target only holds back a month that would otherwise pay a full salary"
	}
	return out
}

func startMonthSummary(t time.Time) string {
	if t.IsZero() {
		return "unset — every month in the ±2 year window is offered"
	}
	return t.Format("2006-01")
}

func enabledIf(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}
