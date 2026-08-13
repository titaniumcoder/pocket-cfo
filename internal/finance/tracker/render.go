package tracker

import (
	"html/template"
	"net/http"

	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

var tmpl = template.Must(template.Must(template.New("").Funcs(template.FuncMap{
	"eur":        formatEuro,
	"truncHours": truncHours,
	"mark":       statusMark,
	"untracked":  untrackedMark,
	"out":        outEuro,
	"outClass":   outClass,
}).Parse(webui.HeaderTemplate)).Parse(templates))

func outEuro(cents int) template.HTML {
	if cents > 0 {
		return template.HTML("&minus;" + formatEuro(cents))
	}
	return template.HTML(formatEuro(-cents))
}

func outClass(cents int) string {
	switch {
	case cents > 0:
		return " neg"
	case cents < 0:
		return " goodamt"
	}
	return ""
}

func statusMark(status string) template.HTML {
	switch status {
	case ActualUnder:
		return template.HTML(markUnder)
	case ActualOver, ActualUnbudgeted, ActualMistimed:
		return template.HTML(markOver)
	}
	return ""
}

func untrackedMark(count int) template.HTML {
	if count <= 0 {
		return ""
	}
	return template.HTML(markUntracked)
}

type loginData struct {
	Error          string
	ShowEmailLogin bool
}

func RenderLogin(w http.ResponseWriter, errMsg string, showEmailLogin bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "login", loginData{Error: errMsg, ShowEmailLogin: showEmailLogin})
}

func RenderEmailLogin(w http.ResponseWriter, showError bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "emailLogin", showError)
}

func RenderEmailSent(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "emailSent", nil)
}

func RenderSpending(w http.ResponseWriter, v SpendingView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "spending", v)
}

func RenderPage(w http.ResponseWriter, f Figures) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.ExecuteTemplate(w, "page", f)
}

var templates = `
{{/* The rate a deduction was charged at, in the middle column on a wide
     screen. One line per schedule: a year the law changed in has two, and
     each carries the months it covered. */}}
{{define "rateMid"}}{{range .}}<span class="rate-line">{{.Rate}}{{if .Span}} <span class="rate-span">{{.Span}}</span>{{end}}</span>{{end}}{{end}}

{{/* The same rate for a narrow screen, where the middle column is hidden and
     its content stacks under the amount instead — the .stack-m idiom every
     folded second figure uses. Rendered always and shown by CSS, so exactly
     one of the two is ever visible. */}}
{{define "rateNarrow"}}{{range .}}<span class="stack-m">{{.Rate}}{{if .Span}} {{.Span}}{{end}}</span>{{end}}{{end}}

{{define "login"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PocketCFO — Login</title>
` + favicon + `
<link rel="stylesheet" href="/static/app.css">
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
<link rel="stylesheet" href="/static/app.css">
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
<link rel="stylesheet" href="/static/app.css">
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

{{define "spending"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PocketCFO — Spending {{.Month}}{{if .UntrackedCount}} &bull;{{end}}</title>
` + favicon + `
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
<main>
  <h1 class="print-title">PocketCFO — Spending {{.Month}}</h1>
  {{template "sitehead" .Header}}

  <nav class="periodnav no-print">
    <div class="periodnav-left"></div>

    <div class="periodnav-center">
      {{with .Nav}}
      {{if .PrevDisabled}}
      <span class="arrow disabled" aria-disabled="true" aria-label="Previous" title="Previous">` + chevronLeft + `</span>
      {{else}}
      <a class="arrow" href="{{.PrevURL}}" aria-label="Previous" title="Previous">` + chevronLeft + `</a>
      {{end}}
      <select id="msel" onchange="navSpending()" aria-label="Month">
        {{$m := .MonthNum}}{{range .Months}}<option value="{{.Num}}"{{if eq .Num $m}} selected{{end}}>{{.Name}}{{if .Untracked}} &bull;{{end}}</option>{{end}}
      </select>
      <select id="ysel" onchange="navSpending()" aria-label="Year">
        {{$y := .Year}}{{range .Years}}<option value="{{.}}"{{if eq . $y}} selected{{end}}>{{.}}</option>{{end}}
      </select>
      {{if .NextDisabled}}
      <span class="arrow disabled" aria-disabled="true" aria-label="Next" title="Next">` + chevronRight + `</span>
      {{else}}
      <a class="arrow" href="{{.NextURL}}" aria-label="Next" title="Next">` + chevronRight + `</a>
      {{end}}
      {{end}}
    </div>

    <div class="periodnav-right">
      <a class="link" href="{{.Nav.TodayURL}}">Today</a>
      <a class="link" href="{{.RefreshURL}}">Reload</a>
    </div>
  </nav>
  <script>
    function navSpending() {
      var y = document.getElementById('ysel').value;
      var m = document.getElementById('msel').value;
      location.href = '/' + y + '/' + m + '/spending';
    }
  </script>

  <section class="panel">
    <h2 class="panel-title">Spending &mdash; {{.Month}}{{if .UntrackedCount}} <span class="untracked-mark" title="cash not yet placed">{{untracked .UntrackedCount}}{{eur .UntrackedCents}} untracked</span>{{end}}</h2>

    {{if .Err}}<p class="error">{{.Err}}</p>{{end}}

    {{if not .Present}}
    <p class="muted">No bank statement has been reconciled for {{.Month}} yet.</p>
    {{else}}

    <h3>Coverage</h3>
    <div class="cov-grid">
      <span class="cov-head">Account</span><span class="cov-head col-secondary">From</span><span class="cov-head col-secondary">To</span><span class="cov-head col-secondary cov-imported">Imported</span>
      {{range .Coverage}}<span>{{.Account}}</span><span class="col-secondary">{{.From}}</span><span class="col-secondary">{{.To}}</span><span class="col-secondary cov-imported">{{.ImportedAt}}</span>{{end}}
    </div>

    <!-- One grid for every transaction on the page. Sections and headings span
         all of it and the rows are display:contents, so each column is a single
         track shared by every row — the columns cannot drift between one
         category and the next, because there is only one set of them. -->
    <div class="spend-grid">
      {{if .Untracked}}
      <h3 class="sg-span sg-untracked">{{untracked .UntrackedCount}}Untracked cash &mdash; {{eur .UntrackedCents}}</h3>
      <p class="sg-span muted">Money that has left the account and has not been decided yet. It is in none of the figures on this page or the dashboard, so until it is placed the month is not finished. Copy a line to tell Hermes what it was.</p>
      <div class="sg-row"><span class="sg-head col-secondary">Date</span><span class="sg-head sg-desc">Description</span><span class="sg-head col-secondary">Note</span><span class="sg-head num">Amount</span><span class="sg-head copy-col no-print"></span></div>
      {{range .Untracked}}<div class="sg-row sg-untracked-row"><span class="col-secondary">{{.Date}}</span><span class="sg-desc">{{.Description}}</span><span class="col-secondary">{{.Reason}}</span><span class="num">{{eur .Cents}}{{if .PartOf}} <span class="part-of">of {{.PartOf}}</span>{{end}}</span><span class="copy-col no-print"><a href="#" class="copy-tx" data-copy="{{.ChangeRequest}}" title="Copy a change request for Hermes" aria-label="Copy a change request for Hermes">` + copyIcon + copiedIcon + `</a></span></div>{{end}}
      {{end}}

      <div class="sg-row"><span class="sg-head col-secondary">Date</span><span class="sg-head sg-desc">Description</span><span class="sg-head col-secondary">Account</span><span class="sg-head num">Amount</span><span class="sg-head copy-col no-print"></span></div>

      {{range .Groups}}
      <h3 class="sg-span">{{.Name}}{{if .Company}} <small>(company)</small>{{end}}</h3>
      {{range .Categories}}
      <h4 class="sg-span sg-cat" id="cat-{{.ID}}">{{.Name}}{{if .Mistimed}} <span class="mistimed-inline">{{mark "mistimed"}}{{.Note}}</span>{{end}}</h4>
      {{range .Transactions}}<div class="sg-row"><span class="col-secondary">{{.Date}}</span><span class="sg-desc">{{.Description}}</span><span class="col-secondary">{{.Account}}</span><span class="num">{{eur .Cents}}{{if .PartOf}} <span class="part-of">of {{.PartOf}}</span>{{end}}</span><span class="copy-col no-print"><a href="#" class="copy-tx" data-copy="{{.ChangeRequest}}" title="Copy a change request for Hermes" aria-label="Copy a change request for Hermes">` + copyIcon + copiedIcon + `</a></span></div>{{end}}
      <div class="sg-row sg-foot"><span class="col-secondary"></span><span class="sg-desc"></span><span class="budget-of col-secondary">(Budget: {{eur .PlannedCents}})</span><span class="num{{if .Status}} flagged{{end}}">{{mark .Status}}{{eur .ActualCents}}</span><span class="copy-col no-print"></span></div>
      {{end}}
      {{end}}

      {{if .Unmatched}}
      <h3 class="sg-span">Not in this month&rsquo;s plan</h3>
      <p class="sg-span muted">These cite a budget category that has no row this month &mdash; renamed, removed, or a one-off whose month has passed.</p>
      <div class="sg-row"><span class="sg-head col-secondary">Date</span><span class="sg-head sg-desc">Description</span><span class="sg-head col-secondary">Category</span><span class="sg-head num">Amount</span><span class="sg-head copy-col no-print"></span></div>
      {{range .Unmatched}}<div class="sg-row"><span class="col-secondary">{{.Date}}</span><span class="sg-desc">{{.Description}}</span><span class="col-secondary">{{.Category}}</span><span class="num">{{eur .Cents}}{{if .PartOf}} <span class="part-of">of {{.PartOf}}</span>{{end}}</span><span class="copy-col no-print"><a href="#" class="copy-tx" data-copy="{{.ChangeRequest}}" title="Copy a change request for Hermes" aria-label="Copy a change request for Hermes">` + copyIcon + copiedIcon + `</a></span></div>{{end}}
      {{end}}

      <h3 class="sg-span">Not budget expenses</h3>
      <p class="sg-span muted">Every statement line deliberately left out of the figures, with the reason &mdash; so this page reconciles to the whole statement and a mis-ignored line is visible.</p>
      {{if .Ignored}}
      <div class="sg-row"><span class="sg-head col-secondary">Date</span><span class="sg-head sg-desc">Description</span><span class="sg-head col-secondary">Reason</span><span class="sg-head num">Amount</span><span class="sg-head copy-col no-print"></span></div>
      {{range .Ignored}}<div class="sg-row"><span class="col-secondary">{{.Date}}</span><span class="sg-desc">{{.Description}}</span><span class="col-secondary">{{.Reason}}</span><span class="num">{{eur .Cents}}{{if .PartOf}} <span class="part-of">of {{.PartOf}}</span>{{end}}</span><span class="copy-col no-print"><a href="#" class="copy-tx" data-copy="{{.ChangeRequest}}" title="Copy a change request for Hermes" aria-label="Copy a change request for Hermes">` + copyIcon + copiedIcon + `</a></span></div>{{end}}
      {{else}}<p class="sg-span muted">None.</p>{{end}}
    </div>

    {{end}}
  </section>
</main>
<script>
  document.addEventListener('click', function (e) {
    var a = e.target.closest('.copy-tx');
    if (!a) return;
    e.preventDefault();

    function flash(state) {
      a.classList.add(state);
      setTimeout(function () { a.classList.remove(state); }, 1500);
    }

    function fallback() {
      var box = document.createElement('textarea');
      box.value = a.dataset.copy;
      box.setAttribute('readonly', '');
      box.style.position = 'fixed';
      box.style.opacity = '0';
      document.body.appendChild(box);
      box.select();
      var ok = false;
      try { ok = document.execCommand('copy'); } catch (err) { ok = false; }
      document.body.removeChild(box);
      flash(ok ? 'copied' : 'failed');
    }

    if (navigator.clipboard) {
      navigator.clipboard.writeText(a.dataset.copy).then(function () { flash('copied'); }, fallback);
    } else {
      fallback();
    }
  });
</script>
</body>
</html>{{end}}

{{define "categoryGroups"}}{{$show := .ShowActuals}}{{$detail := .SpendingDetailURL}}{{range .Groups}}
      <div class="group">
        <div class="group-header" onclick="this.closest('.group').classList.toggle('open')">
          <span class="label">{{.Name}} <span class="chevron">&#9656;</span></span>
          {{if $show}}<span class="mid{{outClass .PlannedCents}}">{{out .PlannedCents}}</span><span class="amt act{{if .HasActual}}{{outClass .ActualCents}}{{end}}{{if .Status}} flagged{{end}}">{{mark .Status}}{{if .HasActual}}{{out .ActualCents}}{{end}}<span class="stack-m">{{if .HasActual}}of {{end}}{{out .PlannedCents}}</span></span>{{else}}<span class="mid"></span><span class="amt{{outClass .PlannedCents}}">{{out .PlannedCents}}</span>{{end}}
        </div>
        <div class="group-rows">
          {{range .Rows}}
          <div class="row{{if .UpcomingMonth}} planned{{end}}{{if .Overridden}} override{{end}}">
            <span class="label">{{.Name}}{{if .Note}} <span class="note">{{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener noreferrer">{{.Note}} <svg class="link-icon" viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg></a>{{else}}{{.Note}}{{end}}</span>{{end}}</span>
{{if $show}}<span class="mid{{if .PlannedCents}}{{outClass .PlannedCents}}{{end}}">{{if .PlannedCents}}{{out .PlannedCents}}{{else if .UpcomingMonth}}{{out .UpcomingCents}} ({{.UpcomingMonth}}){{end}}</span><span class="amt{{if .HasActual}}{{outClass .ActualCents}}{{end}}{{if .ActualStatus}} flagged{{end}}">{{mark .ActualStatus}}{{if .HasActual}}{{if $detail}}<a class="act-link" href="{{$detail}}#cat-{{.CategoryID}}">{{out .ActualCents}}</a>{{else}}{{out .ActualCents}}{{end}}{{end}}{{if .ActualNote}}<span class="act-note">{{.ActualNote}}</span>{{end}}{{if .PlannedCents}}<span class="stack-m">{{if .HasActual}}of {{end}}{{out .PlannedCents}}</span>{{end}}</span>
            {{else}}<span class="mid">{{if and (not .PlannedCents) .UpcomingMonth}}({{.UpcomingMonth}}){{end}}</span><span class="amt{{if .PlannedCents}}{{outClass .PlannedCents}}{{else if .UpcomingMonth}}{{outClass .UpcomingCents}}{{end}}">{{if .PlannedCents}}{{out .PlannedCents}}{{else if .UpcomingMonth}}{{out .UpcomingCents}}{{end}}</span>{{end}}
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
<link rel="stylesheet" href="/static/app.css">
</head>
<body>
<main>
  <h1 class="print-title">PocketCFO — Finance {{.Month}}{{if .UntrackedCount}} &bull;{{end}}</h1>
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
        {{range .Months}}<option value="{{.Num}}"{{if eq .Num $.MonthNum}} selected{{end}}>{{.Name}}{{if .Untracked}} &bull;{{end}}</option>{{end}}
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
      {{if and .UntrackedCount .SpendingDetailURL}}<a class="link untracked-mark" href="{{.SpendingDetailURL}}" title="cash not yet placed">{{untracked .UntrackedCount}}untracked</a>{{end}}
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
      {{else}}{{range .Tracked}}<div class="row"><span class="label">{{.Project}}</span><span class="mid">{{.Hours}} h &times; {{.Rate}}</span><span class="amt">{{eur .AmountCents}}<span class="stack-m">({{.Hours}}h)</span></span></div>{{end}}{{end}}
      {{end}}

      {{if or .ShowExpected .ExpectedErr}}
      <h2>Expected</h2>
      {{if .ExpectedErr}}<div class="row"><span class="error">{{.ExpectedErr}}</span></div>
      {{else}}
      <div class="row"><span class="label">{{.ExpectedRange}}</span><span class="mid">{{.ExpectedHours}} &times; {{.ExpectedRate}}</span><span class="amt">{{eur .ExpectedCents}}<span class="stack-m">({{.ExpectedHours}}h)</span></span></div>
      {{if .ShowVacation}}
      <div class="row"><span class="label">Vacation</span><span class="mid">{{.VacationHoursDeducted}} &times; {{.ExpectedRate}}</span><span class="amt neg">&minus;{{eur .VacationCentsDeducted}}<span class="stack-m">({{.VacationHoursDeducted}}h)</span></span></div>
      <div class="row sub"><span class="label">Expected total</span><span class="mid">{{.ExpectedNetHours}} &times; {{.ExpectedRate}}</span><span class="amt goodamt">{{eur .ExpectedNetCents}}<span class="stack-m">({{.ExpectedNetHours}}h)</span></span></div>
      {{end}}
      {{end}}
      {{end}}

      {{if .TotalErr}}
      <div class="row"><span class="error">{{.TotalErr}}</span></div>
      {{else}}
      <div class="row net"><span class="label">Income{{if .SpendableLabel}} <small>(for {{if .SpendableURL}}<a class="period-link" href="{{.SpendableURL}}">{{.SpendableLabel}}</a>{{else}}{{.SpendableLabel}}{{end}})</small>{{end}}</span><span class="mid">{{.TotalHours}} h &times; {{.TotalRate}}</span><span class="amt netamt">{{eur .TotalCents}}<span class="stack-m">({{.TotalHours}}h)</span></span></div>
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

    <div class="ledger{{if .ShowActuals}} with-actuals{{end}}">
      <h2>Personal income (Bulgaria)</h2>
      {{with .FundingPersonal}}
      {{if .Err}}<div class="row"><span class="error">{{.Err}}</span></div>
      {{else}}
      <div class="row net{{if not $.Invoiced}} gap-below{{end}}"><span class="label">Company income{{if and .FundingLabel (not $.Invoiced)}} <small>(from {{if .FundingURL}}<a class="period-link" href="{{.FundingURL}}">{{.FundingLabel}}</a>{{else}}{{.FundingLabel}}{{end}})</small>{{end}}</span><span class="mid"></span><span class="amt goodamt">{{eur .CompanyIncomeCents}}</span></div>
      {{range $.Invoiced}}<div class="row acct"><span class="label">{{if .URL}}<a href="{{.URL}}">{{.Number}}</a>{{else}}{{.Number}}{{end}} <span class="note">invoiced, usable this month</span></span><span class="mid"></span><span class="amt">{{eur .AmountCents}}</span></div>{{end}}
      {{if .ShowCompanyBalance}}
      <div class="row{{if lt .CompanyOpeningCents 0}} neg{{end}}"><span class="label">In the company{{if $.CompanyBalanceLabel}} <small>({{$.CompanyBalanceLabel}})</small>{{end}}</span><span class="mid"></span><span class="amt{{if lt .CompanyOpeningCents 0}} neg{{else}} goodamt{{end}}">{{eur .CompanyOpeningCents}}</span></div>
      {{range $.CompanyAccounts}}<div class="row acct"><span class="label">{{.Name}} <span class="note">as of {{.AsOf}}{{if .Note}} &middot; {{.Note}}{{end}}</span></span><span class="mid"></span><span class="amt">{{eur .Cents}}</span></div>{{end}}
      {{end}}
      {{if $.Invoiced}}<div class="row gap-below"></div>{{end}}
      {{if $.ShowActuals}}<div class="row colhead"><span class="label"></span><span class="mid">Planned</span><span class="amt">Actual</span></div>{{end}}
      {{template "categoryGroups" $.CompanyLedger}}
      {{if $.ShowActuals}}{{if $.CompanyUnmatchedCents}}<div class="row"><span class="label">Not in this month&rsquo;s plan</span><span class="mid"></span><span class="amt unbudgeted">{{eur $.CompanyUnmatchedCents}}</span></div>{{end}}{{end}}
      {{if .NoLegislation}}<div class="row{{if .CompanyGroups}} gap-above{{end}}"><span class="stale-note">No legislation is in force for this period, so nothing is contributed or taxed and no minimum wage applies. The figures below are zero because config.json states none, not because none is owed.</span></div>{{end}}
      {{if eq .Mode "none"}}<div class="row{{if and .CompanyGroups (not .NoLegislation)}} gap-above{{end}}"><span class="stale-note">No salary is drawn this period, so nothing is contributed or taxed. The company income above stays in the company.</span></div>{{end}}
      {{if .MixedMonthsNote}}<div class="row{{if and .CompanyGroups (not .NoLegislation)}} gap-above{{end}}"><span class="stale-note">{{.MixedMonthsNote}}</span></div>{{end}}
      <div class="row{{if and .CompanyGroups (not .NoLegislation)}} gap-above{{end}}"><span class="label">Employer social</span><span class="mid rate">{{template "rateMid" .EmployerRate}}</span><span class="amt neg">{{if .EmployerContribCents}}&minus;{{end}}{{eur .EmployerContribCents}}{{template "rateNarrow" .EmployerRate}}</span></div>
      <div class="row sub"><span class="label">Gross salary{{if .HeldForTarget}} <small>(minimum until the company reaches {{eur .CompanyTargetCents}}{{if .MinimumWageCents}}, {{eur .MinimumWageCents}}/month{{end}})</small>{{else if eq .Mode "minimum"}} <small>(minimum by choice{{if .MinimumWageCents}}, {{eur .MinimumWageCents}}/month{{end}})</small>{{else if eq .Mode "fixed"}} <small>(fixed by choice, {{eur .FixedSalaryCents}}/month)</small>{{else if eq .Mode "none"}} <small>(no salary drawn)</small>{{else if .MinimumEnforced}} <small>(statutory minimum{{if .MinimumWageCents}}, {{eur .MinimumWageCents}}/month{{end}})</small>{{end}}</span><span class="mid"></span><span class="amt total">{{eur .GrossSalaryCents}}</span></div>
      <div class="row"><span class="label">Employee social</span><span class="mid rate">{{template "rateMid" .EmployeeRate}}</span><span class="amt neg">{{if .EmployeeContribCents}}&minus;{{end}}{{eur .EmployeeContribCents}}{{template "rateNarrow" .EmployeeRate}}</span></div>
      <div class="row"><span class="label">Income tax</span><span class="mid rate">{{template "rateMid" .IncomeTaxRate}}</span><span class="amt neg">{{if .IncomeTaxCents}}&minus;{{end}}{{eur .IncomeTaxCents}}{{template "rateNarrow" .IncomeTaxRate}}</span></div>
      <div class="row net neg"><span class="label">Total company expenses</span>{{if $.ShowActuals}}<span class="mid{{outClass .CompanyExpensesCents}}">{{out .CompanyExpensesCents}}</span><span class="amt{{outClass $.CompanyActualCents}}">{{out $.CompanyActualCents}}<span class="stack-m">of {{out .CompanyExpensesCents}}</span></span>{{else}}<span class="mid"></span><span class="amt neg">&minus;{{eur .CompanyExpensesCents}}</span>{{end}}</div>
      <div class="row net{{if lt .NetIncomeCents 0}} neg{{end}}"><span class="label">Net income</span><span class="mid"></span><span class="amt netamt">{{eur .NetIncomeCents}}</span></div>
      {{if .ShowCompanyBalance}}<div class="row{{if lt .CompanyClosingCents 0}} neg{{end}}"><span class="label">Left in the company{{if .CompanyTargetCents}} <small>(target {{eur .CompanyTargetCents}})</small>{{end}}</span><span class="mid"></span><span class="amt{{if lt .CompanyClosingCents 0}} neg{{end}}">{{eur .CompanyClosingCents}}</span></div>{{end}}
      {{if .TargetNote}}<div class="row"><span class="stale-note">{{.TargetNote}}</span></div>{{end}}
      {{if $.TargetNeedsBalanceNote}}<div class="row"><span class="stale-note">{{$.TargetNeedsBalanceNote}}</span></div>{{end}}
      {{if .CompanyOverdrawnNote}}<div class="row"><span class="stale-note">{{.CompanyOverdrawnNote}}</span></div>{{end}}
      {{end}}
      {{end}}
      {{if .AccountsErr}}<div class="row"><span class="error">{{.AccountsErr}}</span></div>{{end}}
      {{if .ShowOpeningBalance}}
      <div class="row net{{if lt .OpeningBalanceCents 0}} neg{{end}}"><span class="label">Private opening balance <small>({{.OpeningBalanceLabel}})</small></span><span class="mid"></span><span class="amt netamt">{{eur .OpeningBalanceCents}}</span></div>
      {{range .PrivateAccounts}}<div class="row acct"><span class="label">{{.Name}} <span class="note">as of {{.AsOf}}{{if .Note}} &middot; {{.Note}}{{end}}</span></span><span class="mid"></span><span class="amt">{{eur .Cents}}</span></div>{{end}}
      {{if .AccountsStaleNote}}<div class="row"><span class="stale-note">{{.AccountsStaleNote}}</span></div>{{end}}
      <div class="row net gap-above{{if lt .AvailableCents 0}} neg{{end}}"><span class="label">Available to spend</span><span class="mid"></span><span class="amt netamt">{{eur .AvailableCents}}</span></div>
      {{end}}
    </div>

    {{if .Mistimed}}<div class="ledger">
      {{range .Mistimed}}<div class="row"><span class="mistimed-note">{{mark "mistimed"}}{{.Name}} &mdash; {{eur .Cents}}, {{.Note}}. Fix the date in budget.json.</span></div>
      {{end}}
    </div>{{end}}

    <div class="ledger{{if .ShowActuals}} with-actuals{{end}}">
      {{if .BudgetErr}}<div class="row"><span class="error">{{.BudgetErr}}</span></div>
      {{else}}
      {{if .ActualsErr}}<div class="row"><span class="error">{{.ActualsErr}}</span></div>{{end}}
      {{if .ShowActuals}}<div class="row colhead"><span class="label"></span><span class="mid">Planned</span><span class="amt">Actual</span></div>
      {{end}}
      {{template "categoryGroups" .PrivateLedger}}
      {{if .ShowActuals}}{{if .PrivateUnmatchedCents}}
      <div class="row"><span class="label">Not in this month&rsquo;s plan</span><span class="mid"></span><span class="amt unbudgeted">{{eur .PrivateUnmatchedCents}}</span></div>
      {{end}}{{end}}
      {{end}}
    </div>

    {{if .ShowBalance}}
    <div class="ledger">
      <div class="row net neg"><span class="label">Total private expenses</span>{{if .ShowActuals}}<span class="mid{{outClass .PrivateTotalPlannedCents}}">{{out .PrivateTotalPlannedCents}}</span><span class="amt{{outClass .PrivateActualCents}}">{{out .PrivateActualCents}}<span class="stack-m">of {{out .PrivateTotalPlannedCents}}</span></span>{{else}}<span class="mid"></span><span class="amt neg">&minus;{{eur .PrivateTotalPlannedCents}}</span>{{end}}</div>
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

const githubMark = `<svg class="gh-mark" viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"/></svg>`

const markUnder = `<svg class="mark" viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><title>under budget</title><circle cx="12" cy="12" r="9"/><path d="M8.5 14.5s1.3 1.8 3.5 1.8 3.5-1.8 3.5-1.8"/><line x1="9" y1="9.5" x2="9.01" y2="9.5"/><line x1="15" y1="9.5" x2="15.01" y2="9.5"/></svg>`

const markUntracked = `<svg class="mark" viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><title>untracked cash</title><circle cx="12" cy="12" r="9"/><path d="M9.2 9.3a2.9 2.9 0 0 1 5.6 1c0 1.9-2.8 2.4-2.8 4"/><line x1="12" y1="17.5" x2="12.01" y2="17.5"/></svg>`

const markOver = `<svg class="mark" viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><title>over budget</title><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><line x1="12" y1="9.5" x2="12" y2="13.5"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>`

const copyIcon = `<svg class="i-copy" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`

const copiedIcon = `<svg class="i-done" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="20 6 9 17 4 12"/></svg>`

const chevronLeft = `<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="15 18 9 12 15 6"/></svg>`

const chevronRight = `<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="9 18 15 12 9 6"/></svg>`

const favicon = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA2NCA2NCI+PHJlY3Qgd2lkdGg9IjY0IiBoZWlnaHQ9IjY0IiByeD0iMTQiIGZpbGw9IiMxYTczZTgiLz48cmVjdCB4PSIxNCIgeT0iMzYiIHdpZHRoPSI5IiBoZWlnaHQ9IjE2IiByeD0iMiIgZmlsbD0iI2ZmZiIvPjxyZWN0IHg9IjI3LjUiIHk9IjI2IiB3aWR0aD0iOSIgaGVpZ2h0PSIyNiIgcng9IjIiIGZpbGw9IiNmZmYiLz48cmVjdCB4PSI0MSIgeT0iMTQiIHdpZHRoPSI5IiBoZWlnaHQ9IjM4IiByeD0iMiIgZmlsbD0iI2ZmZiIvPjwvc3ZnPgo=">`
