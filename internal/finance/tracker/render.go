package tracker

import (
	"html/template"
	"net/http"

	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

var tmpl = template.Must(template.Must(template.New("").Funcs(template.FuncMap{
	"eur":        formatEuro,
	"truncHours": truncHours,
}).Parse(webui.HeaderTemplate)).Parse(templates))

// loginData is the login template's data: errMsg is shown when non-empty;
// showEmailLogin hides the "Continue with email" option entirely when no
// email addresses are allowlisted (see Server.EmailAllowedAddresses) — that
// path can never succeed with an empty allowlist, so there's no point
// showing it.
type loginData struct {
	Error          string
	ShowEmailLogin bool
}

// RenderLogin renders the login page. errMsg is shown when non-empty.
// showEmailLogin controls whether the "Continue with email" option appears.
func RenderLogin(w http.ResponseWriter, errMsg string, showEmailLogin bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "login", loginData{Error: errMsg, ShowEmailLogin: showEmailLogin})
}

// RenderEmailLogin renders the email-entry form for the email-login flow.
// showError is set when the visitor arrived via an invalid or expired
// callback link.
func RenderEmailLogin(w http.ResponseWriter, showError bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "emailLogin", showError)
}

// RenderEmailSent renders the "check your email" confirmation shown after
// submitting the email-login form, regardless of whether the address was
// actually allowlisted (see Server.handleEmailLoginRequest).
func RenderEmailSent(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "emailSent", nil)
}

// RenderPage renders the full dashboard page — chrome, navigation, and the
// computed ledger — in one response. f must already be fully computed (see
// Tracker.ComputeMonth/ComputeYear); there's no separate placeholder/loading
// state to render.
func RenderPage(w http.ResponseWriter, f Figures) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "page", f)
}

var templates = `
{{define "login"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PocketCFO — Login</title>
` + favicon + `
<link rel="stylesheet" href="/invoicing/static/app.css">
</head>
<body>
<main class="login">
  <h1>PocketCFO</h1>
  <a class="button-outline" href="/auth/login">` + githubMark + `Continue with GitHub</a>
  {{if .ShowEmailLogin}}<p class="login-alt"><a class="link" href="/auth/email">or continue with email</a></p>{{end}}
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
</main>
</body>
</html>{{end}}

{{define "emailLogin"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PocketCFO — Login</title>
` + favicon + `
<link rel="stylesheet" href="/invoicing/static/app.css">
</head>
<body>
<main class="login">
  <h1>PocketCFO</h1>
  <form method="post" action="/auth/email">
    <input type="email" name="email" placeholder="you@example.com" required autofocus>
    <button class="button" type="submit">Email me a login link</button>
  </form>
  {{if .}}<p class="error">That link is invalid or has expired — request a new one above.</p>{{end}}
  <p><a class="link" href="/">&larr; Back</a></p>
</main>
</body>
</html>{{end}}

{{define "emailSent"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PocketCFO — Login</title>
` + favicon + `
<link rel="stylesheet" href="/invoicing/static/app.css">
</head>
<body>
<main class="login">
  <h1>PocketCFO</h1>
  <p>If that address is authorized, a login link is on its way — check your inbox.</p>
  <p>The link expires shortly, so use it soon.</p>
  <p><a class="link" href="/">&larr; Back</a></p>
</main>
</body>
</html>{{end}}

{{define "categoryGroups"}}{{range .}}
      <div class="group">
        <div class="group-header" onclick="this.closest('.group').classList.toggle('open')">
          <span class="label">{{.Name}} <span class="chevron">&#9656;</span></span>
          <span class="amt neg">&minus;{{eur .PlannedCents}}</span>
        </div>
        <div class="group-rows">
          {{range .Rows}}
          <div class="row{{if .UpcomingMonth}} planned{{end}}{{if .Overridden}} override{{end}}">
            <span class="label">{{.Name}}{{if .Note}} <span class="note">{{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Note}} <svg class="link-icon" viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg></a>{{else}}{{.Note}}{{end}}</span>{{end}}</span>
            <span class="mid">{{if .PlannedCents}}{{eur .PlannedCents}}{{else if .UpcomingMonth}}{{eur .UpcomingCents}} ({{.UpcomingMonth}}){{end}}</span>
            <span class="amt"></span>
          </div>
          {{end}}
        </div>
      </div>
{{end}}{{end}}

{{define "page"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
{{if .TogglPending}}<meta http-equiv="refresh" content="` + pendingRefresh + `">{{end}}
<title>PocketCFO — Finance</title>
` + favicon + `
<link rel="stylesheet" href="/invoicing/static/app.css">
</head>
<body>
<main>
  <h1 class="print-title">PocketCFO — Finance {{.Month}}</h1>
  {{template "sitehead" .Header}}

  <div class="layout">
  <div class="main-col">

  <nav class="periodnav no-print">
    <div class="periodnav-left">
      <div class="segmented">
        <a href="{{.MonthViewURL}}"{{if eq .Mode "month"}} class="active"{{end}}>Month</a>
        <a href="{{.YearViewURL}}"{{if eq .Mode "year"}} class="active"{{end}}>Year</a>
      </div>
    </div>

    <div class="periodnav-center">
      {{if .PrevDisabled}}
      <span class="arrow disabled" aria-disabled="true" aria-label="Previous" title="Previous">` + chevronLeft + `</span>
      {{else}}
      <a class="arrow" href="{{.PrevURL}}" aria-label="Previous" title="Previous">` + chevronLeft + `</a>
      {{end}}
      {{if eq .Mode "year"}}
      <select id="ysel" onchange="navYear()" aria-label="Year">
        {{range .Years}}<option value="{{.}}"{{if eq . $.Year}} selected{{end}}>{{.}}</option>{{end}}
      </select>
      {{else}}
      <select id="msel" onchange="navMonth()" aria-label="Month">
        {{range .Months}}<option value="{{.Num}}"{{if eq .Num $.MonthNum}} selected{{end}}>{{.Name}}</option>{{end}}
      </select>
      <select id="ysel" onchange="navMonth()" aria-label="Year">
        {{range .Years}}<option value="{{.}}"{{if eq . $.Year}} selected{{end}}>{{.}}</option>{{end}}
      </select>
      {{end}}
      {{if .NextDisabled}}
      <span class="arrow disabled" aria-disabled="true" aria-label="Next" title="Next">` + chevronRight + `</span>
      {{else}}
      <a class="arrow" href="{{.NextURL}}" aria-label="Next" title="Next">` + chevronRight + `</a>
      {{end}}
    </div>

    <div class="periodnav-right">
      <a class="link" href="{{.TodayURL}}">Today</a>
      <a class="link" href="{{.RefreshURL}}">Reload</a>
    </div>
  </nav>
  <script>
    function navMonth() {
      var y = document.getElementById('ysel').value;
      var m = document.getElementById('msel').value;
      location.href = '/' + y + '/' + m;
    }
    function navYear() {
      location.href = '/' + document.getElementById('ysel').value;
    }
  </script>

  <section class="panel income-panel">
    <h2 class="panel-title">Income</h2>
    <div class="ledger">
      {{if .TogglPending}}
      <div class="row"><span class="stale-note">Fetching tracked hours from Toggl — this page refreshes itself in a moment.</span></div>
      {{else}}
      {{if .TogglStaleNote}}<div class="row"><span class="stale-note">{{.TogglStaleNote}}</span></div>{{end}}
      {{if or .TrackedErr .Tracked}}
      <h2>Tracked</h2>
      {{if .TrackedErr}}<div class="row"><span class="error">{{.TrackedErr}}</span></div>
      {{else}}{{range .Tracked}}<div class="row"><span class="label">{{.Project}}</span><span class="mid">{{.Hours}} h &times; {{.Rate}}</span><span class="amt">{{eur .AmountCents}}<span class="hrs-m">({{.Hours}}h)</span></span></div>{{end}}{{end}}
      {{end}}

      {{if or .ShowExpected .ExpectedErr}}
      <h2>Expected</h2>
      {{if .ExpectedErr}}<div class="row"><span class="error">{{.ExpectedErr}}</span></div>
      {{else}}
      <div class="row"><span class="label">{{.ExpectedRange}}</span><span class="mid">{{.ExpectedHours}} &times; {{.ExpectedRate}}</span><span class="amt">{{eur .ExpectedCents}}<span class="hrs-m">({{.ExpectedHours}}h)</span></span></div>
      {{if .ShowVacation}}
      <div class="row"><span class="label">Vacation</span><span class="mid">{{.VacationHoursDeducted}} &times; {{.ExpectedRate}}</span><span class="amt neg">&minus;{{eur .VacationCentsDeducted}}<span class="hrs-m">({{.VacationHoursDeducted}}h)</span></span></div>
      <div class="row sub"><span class="label">Expected total</span><span class="mid">{{.ExpectedNetHours}} &times; {{.ExpectedRate}}</span><span class="amt goodamt">{{eur .ExpectedNetCents}}<span class="hrs-m">({{.ExpectedNetHours}}h)</span></span></div>
      {{end}}
      {{end}}
      {{end}}

      {{if .TotalErr}}
      <div class="row"><span class="error">{{.TotalErr}}</span></div>
      {{else}}
      <div class="row net"><span class="label">Income{{if .SpendableLabel}} <small>(for {{if .SpendableURL}}<a class="period-link" href="{{.SpendableURL}}">{{.SpendableLabel}}</a>{{else}}{{.SpendableLabel}}{{end}})</small>{{end}}</span><span class="mid">{{.TotalHours}} h &times; {{.TotalRate}}</span><span class="amt netamt">{{eur .TotalCents}}<span class="hrs-m">({{.TotalHours}}h)</span></span></div>
      {{end}}
      {{end}}
    </div>
  </section>

  <section class="panel budget-panel">
    <div class="panel-title-row">
      <h2 class="panel-title">Rolling budget</h2>
      {{if eq .Mode "month"}}
      <a class="minimal-toggle no-print{{if .MinimalMode}} active{{end}}" href="{{.MinimalToggleURL}}" role="switch" aria-checked="{{if .MinimalMode}}true{{else}}false{{end}}">
        <span class="minimal-toggle-track"><span class="minimal-toggle-thumb"></span></span>
        <span class="minimal-toggle-label">Minimal</span>
      </a>
      {{end}}
    </div>

    <div class="ledger">
      <h2>Personal income (Bulgaria)</h2>
      {{with .FundingPersonal}}
      {{if .Err}}<div class="row"><span class="error">{{.Err}}</span></div>
      {{else}}
      <div class="row net{{if not $.Invoiced}} gap-below{{end}}"><span class="label">Company income{{if and .FundingLabel (not $.Invoiced)}} <small>(from {{if .FundingURL}}<a class="period-link" href="{{.FundingURL}}">{{.FundingLabel}}</a>{{else}}{{.FundingLabel}}{{end}})</small>{{end}}</span><span class="mid"></span><span class="amt goodamt">{{eur .CompanyIncomeCents}}</span></div>
      {{range $.Invoiced}}<div class="row acct"><span class="label">{{if .URL}}<a href="{{.URL}}">{{.Number}}</a>{{else}}{{.Number}}{{end}} <span class="note">invoiced, usable this month</span></span><span class="mid"></span><span class="amt">{{eur .AmountCents}}</span></div>{{end}}
      {{if $.Invoiced}}<div class="row gap-below"></div>{{end}}
      {{template "categoryGroups" .CompanyGroups}}
      <div class="row{{if .CompanyGroups}} gap-above{{end}}"><span class="label">Employer social ({{.EmployerPct}}%)</span><span class="mid"></span><span class="amt neg">&minus;{{eur .EmployerContribCents}}</span></div>
      <div class="row sub"><span class="label">Gross salary</span><span class="mid"></span><span class="amt total">{{eur .GrossSalaryCents}}</span></div>
      <div class="row"><span class="label">Employee social ({{.EmployeePct}}%)</span><span class="mid"></span><span class="amt neg">&minus;{{eur .EmployeeContribCents}}</span></div>
      <div class="row"><span class="label">Income tax ({{.IncomeTaxPct}}%)</span><span class="mid"></span><span class="amt neg">&minus;{{eur .IncomeTaxCents}}</span></div>
      <div class="row net neg"><span class="label">Total company expenses</span><span class="mid"></span><span class="amt neg">&minus;{{eur .CompanyExpensesCents}}</span></div>
      <div class="row net{{if lt .NetIncomeCents 0}} neg{{end}}"><span class="label">Net income</span><span class="mid"></span><span class="amt netamt">{{eur .NetIncomeCents}}</span></div>
      {{end}}
      {{end}}
      {{if .AccountsErr}}<div class="row"><span class="error">{{.AccountsErr}}</span></div>{{end}}
      {{if .ShowOpeningBalance}}
      <div class="row net{{if lt .OpeningBalanceCents 0}} neg{{end}}"><span class="label">Opening balance <small>({{.OpeningBalanceLabel}})</small></span><span class="mid"></span><span class="amt netamt">{{eur .OpeningBalanceCents}}</span></div>
      {{range .PrivateAccounts}}<div class="row acct"><span class="label">{{.Name}} <span class="note">as of {{.AsOf}}{{if .Note}} &middot; {{.Note}}{{end}}</span></span><span class="mid"></span><span class="amt">{{eur .Cents}}</span></div>{{end}}
      {{if .AccountsStaleNote}}<div class="row"><span class="stale-note">{{.AccountsStaleNote}}</span></div>{{end}}
      <div class="row net gap-above{{if lt .AvailableCents 0}} neg{{end}}"><span class="label">Available to spend</span><span class="mid"></span><span class="amt netamt">{{eur .AvailableCents}}</span></div>
      {{end}}
    </div>

    <div class="ledger">
      {{if .BudgetErr}}<div class="row"><span class="error">{{.BudgetErr}}</span></div>
      {{else}}
      {{template "categoryGroups" .PrivateGroups}}
      {{end}}
    </div>

    {{if .ShowBalance}}
    <div class="ledger">
      <div class="row net neg"><span class="label">Total private expenses</span><span class="mid"></span><span class="amt neg">&minus;{{eur .PrivateTotalPlannedCents}}</span></div>
      <div class="row net balance{{if lt .BalanceCents 0}} neg{{end}}"><span class="label">Balance</span><span class="mid"></span><span class="amt netamt">{{eur .BalanceCents}}</span></div>
    </div>
    {{end}}
  </section>

  </div>

  <aside class="side-col holidays">
    <div class="holidays-content">
      <h2>Holidays</h2>
      {{if .HolidaysErr}}<p class="error">{{.HolidaysErr}}</p>
      {{else if .Holidays}}<ul>{{range .Holidays}}<li{{if .Current}} class="hday-current"{{end}}><span class="hday-date">{{.Date}}</span><span class="hday-name">{{.Name}}</span></li>{{end}}</ul>
      {{else}}<p class="muted">None this year</p>{{end}}
    </div>
  </aside>
  </div>
</main>
</body>
</html>{{end}}
`

// githubMark is GitHub's own mark, inlined so the login page needs no
// external request (the app links no CDNs anywhere else either). Drawn in
// currentColor so it follows the button's text in both light and dark
// themes.
const githubMark = `<svg class="gh-mark" viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"/></svg>`

// chevronLeft/chevronRight replace the guillemets the period arrows used
// to draw: a real icon centres on its own box, so it lines up with the
// month/year selects instead of riding high on the text baseline.
const chevronLeft = `<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="15 18 9 12 15 6"/></svg>`

const chevronRight = `<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="9 18 15 12 9 6"/></svg>`

// favicon is a small inline SVG (a blue rounded-square with an ascending
// three-bar chart), embedded as a base64 data URI so no static file route or
// build step is needed.
const favicon = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA2NCA2NCI+PHJlY3Qgd2lkdGg9IjY0IiBoZWlnaHQ9IjY0IiByeD0iMTQiIGZpbGw9IiMxYTczZTgiLz48cmVjdCB4PSIxNCIgeT0iMzYiIHdpZHRoPSI5IiBoZWlnaHQ9IjE2IiByeD0iMiIgZmlsbD0iI2ZmZiIvPjxyZWN0IHg9IjI3LjUiIHk9IjI2IiB3aWR0aD0iOSIgaGVpZ2h0PSIyNiIgcng9IjIiIGZpbGw9IiNmZmYiLz48cmVjdCB4PSI0MSIgeT0iMTQiIHdpZHRoPSI5IiBoZWlnaHQ9IjM4IiByeD0iMiIgZmlsbD0iI2ZmZiIvPjwvc3ZnPgo=">`
