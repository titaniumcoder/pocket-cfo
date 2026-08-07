package main

import (
	"strconv"
	"strings"
)

// configRow is one setting as shown on /info. Value is already
// display-ready — secrets arrive here masked (see maskSecret), never raw,
// so nothing downstream can leak one by accident.
type configRow struct {
	Name   string
	Value  string
	Secret bool
}

// configGroup is a titled block of settings on the /info config panel.
type configGroup struct {
	Name string
	Rows []configRow
}

// unsetLabel marks a setting with no value, so an empty cell can't be
// mistaken for "set to something blank".
const unsetLabel = "(unset)"

// maskSecret renders a credential as its first 3 and last 2 characters with
// the middle starred out — enough to tell *which* key is configured (and to
// spot a truncated paste) without putting the secret itself on screen.
//
// Short values are starred out entirely rather than partially revealed: at
// 8 characters the 3+2 rule would already expose more than half, and at 5 or
// fewer it would expose the whole thing. Counting is in runes, so a
// multi-byte character can't be sliced in half into mojibake.
func maskSecret(v string) string {
	if v == "" {
		return unsetLabel
	}
	r := []rune(v)
	const shown = 5 // 3 leading + 2 trailing
	if len(r) <= shown*2 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:3]) + strings.Repeat("*", len(r)-shown) + string(r[len(r)-2:])
}

// orUnset renders a plain (non-secret) value, marking the empty case.
func orUnset(v string) string {
	if v == "" {
		return unsetLabel
	}
	return v
}

// configGroups is the current, fully-resolved configuration as rendered at
// the top of /info — env vars merged with config.json, exactly as this
// running process resolved them (see loadConfig and
// internal/finance/config.Load), rather than what any file currently says
// on disk. Every credential goes through maskSecret; the page is
// admin-only regardless (see handleInfo), but masking means a screenshot
// or a shoulder-surfer still can't walk away with a key.
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
			{Name: "socialEmployerRate", Value: strconv.FormatFloat(f.EmployerRate, 'f', -1, 64)},
			{Name: "socialEmployeeRate", Value: strconv.FormatFloat(f.EmployeeRate, 'f', -1, 64)},
			{Name: "socialMaxInsurableMonthly", Value: strconv.FormatFloat(f.MaxInsurableMonthly, 'f', -1, 64)},
			{Name: "incomeTaxRate", Value: strconv.FormatFloat(f.IncomeTaxRate, 'f', -1, 64)},
		}},
	}
}
