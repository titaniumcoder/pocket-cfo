package tracker

import (
	_ "embed"
	"html/template"
	"net/http"
)

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"eur":        formatEuro,
	"truncHours": truncHours,
}).Parse(templates))

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
<title>Finance Tracker — Login</title>
` + favicon + `
<style>` + css + `</style>
</head>
<body>
<main class="login">
  <h1>Finance Tracker</h1>
  <a class="button" href="/auth/login">Continue with GitHub</a>
  {{if .ShowEmailLogin}}<a class="button" href="/auth/email">Continue with email</a>{{end}}
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
</main>
</body>
</html>{{end}}

{{define "emailLogin"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Finance Tracker — Login</title>
` + favicon + `
<style>` + css + `</style>
</head>
<body>
<main class="login">
  <h1>Finance Tracker</h1>
  <form method="post" action="/auth/email">
    <input type="email" name="email" placeholder="you@example.com" required autofocus>
    <button class="button" type="submit">Email me a login link</button>
  </form>
  {{if .}}<p class="error">That link is invalid or has expired — request a new one above.</p>{{end}}
  <p><a class="link" href="/login">&larr; Back</a></p>
</main>
</body>
</html>{{end}}

{{define "emailSent"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Finance Tracker — Login</title>
` + favicon + `
<style>` + css + `</style>
</head>
<body>
<main class="login">
  <h1>Finance Tracker</h1>
  <p>If that address is authorized, a login link is on its way — check your inbox.</p>
  <p>The link expires shortly, so use it soon.</p>
  <p><a class="link" href="/login">&larr; Back</a></p>
</main>
</body>
</html>{{end}}

{{define "categoryGroups"}}{{range .}}
      <div class="group">
        <div class="group-header" onclick="this.closest('.group').classList.toggle('open')">
          <span class="label">{{.Name}} <span class="chevron">&#9656;</span></span>
          <span class="amt neg">&minus;{{eur .SpentCents}}</span>
        </div>
        <div class="group-rows">
          {{range .Rows}}
          <div class="row{{if .PlannedMonth}} planned{{end}}{{if .Overridden}} override{{end}}">
            <span class="label">{{.Name}}{{if .Note}} <span class="note">{{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Note}} <svg class="link-icon" viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg></a>{{else}}{{.Note}}{{end}}</span>{{end}}</span>
            <span class="mid">{{if .SpentCents}}{{eur .SpentCents}}{{else if .PlannedMonth}}{{eur .PlannedCents}} ({{.PlannedMonth}}){{end}}</span>
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
<title>Finance Tracker</title>
` + favicon + `
<style>` + css + `</style>
</head>
<body>
<main>
  <h1 class="print-title">Finance Tracker {{.Month}}</h1>
  <header class="no-print">
    <h1>Finance Tracker</h1>
    <nav class="topnav">
      <a class="active" href="/">Finance</a>
      {{if .ShowInvoicingLink}}<a href="/invoicing">Invoicing</a>{{end}}
    </nav>
    <div class="hdr-right">
      <span class="updated">Updated {{.LastUpdated}}</span>
      <a class="link" href="{{.TodayURL}}">Today</a>
      <a class="link" href="{{.RefreshURL}}">Reload</a>
      <form method="post" action="/auth/logout"><button class="link">Logout</button></form>
    </div>
  </header>

  <nav class="viewtoggle no-print">
    <a href="{{.MonthViewURL}}"{{if eq .Mode "month"}} class="active"{{end}}>Month</a>
    <a href="{{.YearViewURL}}"{{if eq .Mode "year"}} class="active"{{end}}>Year</a>
  </nav>

  <nav class="monthnav no-print">
    {{if .PrevDisabled}}
    <span class="arrow disabled" aria-disabled="true" aria-label="Previous" title="Previous">&laquo;</span>
    {{else}}
    <a class="arrow" href="{{.PrevURL}}" aria-label="Previous" title="Previous">&laquo;</a>
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
    <span class="arrow disabled" aria-disabled="true" aria-label="Next" title="Next">&raquo;</span>
    {{else}}
    <a class="arrow" href="{{.NextURL}}" aria-label="Next" title="Next">&raquo;</a>
    {{end}}
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

  <div class="layout">
  <div class="main-col">

  <section class="panel income-panel">
    <h2 class="panel-title">Income</h2>
    <div class="ledger">
      {{if or .TrackedErr .Tracked}}
      <h2>Tracked</h2>
      {{if .TrackedErr}}<div class="row"><span class="error">{{.TrackedErr}}</span></div>
      {{else}}{{range .Tracked}}<div class="row"><span class="label">{{.Project}}</span><span class="mid">{{.Hours}} h &times; {{.Rate}}</span><span class="amt">{{eur .AmountCents}}<span class="hrs-m">({{.Hours}}h)</span></span></div>{{end}}{{end}}
      {{end}}

      {{if .Invoiced}}
      <h2>Invoiced</h2>
      {{range .Invoiced}}<div class="row"><span class="label">{{.Project}}</span><span class="mid"></span><span class="amt">{{eur .AmountCents}}</span></div>{{end}}
      {{end}}

      <h2>Expected</h2>
      {{if .ExpectedErr}}<div class="row"><span class="error">{{.ExpectedErr}}</span></div>
      {{else}}
      <div class="row"><span class="label">{{.ExpectedRange}}</span><span class="mid">{{.ExpectedHours}} &times; {{.ExpectedRate}}</span><span class="amt">{{eur .ExpectedCents}}<span class="hrs-m">({{.ExpectedHours}}h)</span></span></div>
      {{if .ShowVacation}}
      <div class="row"><span class="label">Vacation</span><span class="mid">{{.VacationHoursDeducted}} &times; {{.ExpectedRate}}</span><span class="amt neg">&minus;{{eur .VacationCentsDeducted}}<span class="hrs-m">({{.VacationHoursDeducted}}h)</span></span></div>
      <div class="row sub"><span class="label">Expected total</span><span class="mid">{{.ExpectedNetHours}} &times; {{.ExpectedRate}}</span><span class="amt goodamt">{{eur .ExpectedNetCents}}<span class="hrs-m">({{.ExpectedNetHours}}h)</span></span></div>
      {{end}}
      {{end}}

      {{if .TotalErr}}
      <div class="row"><span class="error">{{.TotalErr}}</span></div>
      {{else}}
      <div class="row net"><span class="label">Income{{if .SpendableLabel}} <small>(for {{if .SpendableURL}}<a class="period-link" href="{{.SpendableURL}}">{{.SpendableLabel}}</a>{{else}}{{.SpendableLabel}}{{end}})</small>{{end}}</span><span class="mid">{{.TotalHours}} h &times; {{.TotalRate}}</span><span class="amt netamt">{{eur .TotalCents}}<span class="hrs-m">({{.TotalHours}}h)</span></span></div>
      {{end}}
    </div>
  </section>

  <section class="panel expenses-panel">
    <div class="panel-title-row">
      <h2 class="panel-title">Expenses</h2>
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
      <div class="row net gap-below"><span class="label">Company income{{if .FundingLabel}} <small>(from {{if .FundingURL}}<a class="period-link" href="{{.FundingURL}}">{{.FundingLabel}}</a>{{else}}{{.FundingLabel}}{{end}})</small>{{end}}</span><span class="mid"></span><span class="amt goodamt">{{eur .CompanyIncomeCents}}</span></div>
      {{template "categoryGroups" .CompanyGroups}}
      <div class="row{{if .CompanyGroups}} gap-above{{end}}"><span class="label">Employer social ({{.EmployerPct}}%)</span><span class="mid"></span><span class="amt neg">&minus;{{eur .EmployerContribCents}}</span></div>
      <div class="row sub"><span class="label">Gross salary</span><span class="mid"></span><span class="amt total">{{eur .GrossSalaryCents}}</span></div>
      <div class="row"><span class="label">Employee social ({{.EmployeePct}}%)</span><span class="mid"></span><span class="amt neg">&minus;{{eur .EmployeeContribCents}}</span></div>
      <div class="row"><span class="label">Income tax ({{.IncomeTaxPct}}%)</span><span class="mid"></span><span class="amt neg">&minus;{{eur .IncomeTaxCents}}</span></div>
      <div class="row net neg"><span class="label">Total company expenses</span><span class="mid"></span><span class="amt neg">&minus;{{eur .CompanyExpensesCents}}</span></div>
      <div class="row net"><span class="label">Net income</span><span class="mid"></span><span class="amt goodamt">{{eur .NetIncomeCents}}</span></div>
      {{end}}
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
      <div class="row net neg"><span class="label">Total private expenses</span><span class="mid"></span><span class="amt neg">&minus;{{eur .PrivateTotalSpentCents}}</span></div>
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

// favicon is a small inline SVG (a blue rounded-square with an ascending
// three-bar chart), embedded as a base64 data URI so no static file route or
// build step is needed.
const favicon = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA2NCA2NCI+PHJlY3Qgd2lkdGg9IjY0IiBoZWlnaHQ9IjY0IiByeD0iMTQiIGZpbGw9IiMxYTczZTgiLz48cmVjdCB4PSIxNCIgeT0iMzYiIHdpZHRoPSI5IiBoZWlnaHQ9IjE2IiByeD0iMiIgZmlsbD0iI2ZmZiIvPjxyZWN0IHg9IjI3LjUiIHk9IjI2IiB3aWR0aD0iOSIgaGVpZ2h0PSIyNiIgcng9IjIiIGZpbGw9IiNmZmYiLz48cmVjdCB4PSI0MSIgeT0iMTQiIHdpZHRoPSI5IiBoZWlnaHQ9IjM4IiByeD0iMiIgZmlsbD0iI2ZmZiIvPjwvc3ZnPgo=">`

//go:embed style.css
var css string
