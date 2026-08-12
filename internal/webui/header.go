// Package webui holds the chrome shared by every dashboard page. It exists
// because those pages come from two template systems that can't reach each
// other — the finance dashboard from a Go-embedded set, invoicing and info from
// templates/ — so the shared markup lives here as a source string both parse.
// Hand-copying is how the three pages drifted into three different headers.
//
// The matching CSS is in static/app.css, including the header's own bottom
// spacing: the gap below the menu belongs to the menu.
package webui

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Page identifiers for Header.Active — which nav entry renders as current.
const (
	PageFinance   = "finance"
	PageSpending  = "spending"
	PageInvoicing = "invoicing"
	PageInfo      = "info"
)

// Header is what the shared site header renders from. The Show* flags mean
// "this session may reach that page"; cmd/pocketcfo derives them, since only it
// knows the auth rules.
//
// Deliberately identical on every page. Page-specific controls live with the
// content they act on — a header that changes shape between pages reads as a
// different app on each one. There is no read-only marker: every session that
// gets here is read-only, so the badge told nobody anything.
type Header struct {
	Login  string
	Active string

	ShowFinance   bool
	ShowSpending  bool
	ShowInvoicing bool
	ShowInfo      bool

	// Period is the month this page is showing, and every menu link is built
	// from it: changing page should not also change the month you are
	// looking at.
	Period Period
}

// Period is the month a page is currently showing. Pages that have no month
// of their own carry the one they were reached with, in the query string, so
// a round trip through them loses nothing either.
//
// Month is always a real month, even in YearView, because the spending page
// reads one month at a time and needs somewhere to land. YearView records
// that the dashboard was showing a whole year, which is the one case where
// only the year survives the jump.
type Period struct {
	Year     int
	Month    int
	YearView bool
}

// ParsePeriod reads a period back out of a query string. Out-of-range values
// produce a zero Period rather than an error: the finance routes validate the
// year and month they are actually given and redirect if they don't like
// them, so the only job here is to not invent one.
func ParsePeriod(year, month string) Period {
	y, err := strconv.Atoi(year)
	if err != nil || y < 1970 || y > 9999 {
		return Period{}
	}
	m, err := strconv.Atoi(month)
	if err != nil || m < 1 || m > 12 {
		return Period{Year: y, Month: int(time.January), YearView: true}
	}
	return Period{Year: y, Month: m}
}

// Query renders the period for a page that has no route of its own to put it
// in. Empty for a zero Period, so those links stay bare.
func (p Period) Query() string {
	switch {
	case p.Year == 0:
		return ""
	case p.YearView:
		return fmt.Sprintf("?year=%d", p.Year)
	default:
		return fmt.Sprintf("?year=%d&month=%d", p.Year, p.Month)
	}
}

// FinanceHref is the dashboard showing what the current page is showing.
func (p Period) FinanceHref() string {
	switch {
	case p.Year == 0:
		return "/"
	case p.YearView || p.Month == 0:
		return fmt.Sprintf("/%d", p.Year)
	default:
		return fmt.Sprintf("/%d/%d", p.Year, p.Month)
	}
}

// SpendingHref is a month even when the period is a year: a year of statement
// lines is not a page anyone reads.
func (p Period) SpendingHref() string {
	if p.Year == 0 || p.Month == 0 {
		return ""
	}
	return fmt.Sprintf("/%d/%d/spending", p.Year, p.Month)
}

// InvoicingHref filters to the year in view. Invoicing has no month of its
// own, so it carries the month through untouched rather than dropping it —
// otherwise every trip through invoicing would cost you your month.
func (p Period) InvoicingHref() string { return "/invoicing" + p.Query() }

// InfoHref carries the whole period, since info has no period at all.
func (p Period) InfoHref() string { return "/info" + p.Query() }

// AvatarURL is the user's picture: Gravatar for an email login, GitHub's for a
// GitHub one. These are the only external requests any page makes, and the
// Gravatar URL necessarily discloses a hash of the address.
//
// Returning "" falls back to Initials — but so does a URL that fails to load,
// which is the case that actually happens: the image sits over the initials,
// and the template removes it on error rather than leaving the browser to
// paint a broken-image glyph on top of them.
func (h Header) AvatarURL() string {
	login := strings.ToLower(strings.TrimSpace(h.Login))
	if login == "" {
		return ""
	}
	if strings.Contains(login, "@") {
		sum := md5.Sum([]byte(login))
		return "https://www.gravatar.com/avatar/" + hex.EncodeToString(sum[:]) + "?s=64&d=mp"
	}
	return "https://github.com/" + url.PathEscape(login) + ".png?size=64"
}

// Initials render underneath the avatar, so a missing or failed image still
// shows something recognisable rather than a broken-image icon.
func (h Header) Initials() string {
	login := strings.TrimSpace(h.Login)
	if login == "" {
		return "?"
	}
	// An email is identified by its local part, not the domain everyone
	// shares.
	if at := strings.IndexByte(login, '@'); at > 0 {
		login = login[:at]
	}
	fields := strings.FieldsFunc(login, func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == ' ' || r == '+'
	})
	if len(fields) == 0 {
		return strings.ToUpper(login[:1])
	}
	if len(fields) == 1 {
		f := []rune(fields[0])
		if len(f) == 1 {
			return strings.ToUpper(string(f[0]))
		}
		return strings.ToUpper(string(f[0])) + strings.ToLower(string(f[1]))
	}
	return strings.ToUpper(string([]rune(fields[0])[0])) + strings.ToUpper(string([]rune(fields[1])[0]))
}

// HasNav reports whether the top menu is worth rendering at all: it takes
// two or more reachable pages for a menu to offer navigation. A session
// scoped to a single page gets no menu, rather than a bar containing only
// the page it's already on.
func (h Header) HasNav() bool {
	n := 0
	for _, shown := range []bool{h.ShowFinance, h.ShowSpending, h.ShowInvoicing, h.ShowInfo} {
		if shown {
			n++
		}
	}
	return n > 1
}

// IsActive reports whether page is the one currently being viewed.
func (h Header) IsActive(page string) bool { return h.Active == page }

// HeaderTemplate is the shared {{define "sitehead"}} block. Parse it into a
// template set alongside that set's own pages, then invoke it with a Header
// value: {{template "sitehead" .Header}}.
const HeaderTemplate = `
{{define "sitehead"}}<header class="no-print">
  <h1>PocketCFO</h1>
  {{if .HasNav}}
  <nav class="topnav">
    {{if .ShowFinance}}<a{{if .IsActive "finance"}} class="active"{{end}} href="{{.Period.FinanceHref}}">Finance</a>{{end}}
    {{if .ShowSpending}}<a{{if .IsActive "spending"}} class="active"{{end}} href="{{.Period.SpendingHref}}">Spending</a>{{end}}
    {{if .ShowInvoicing}}<a{{if .IsActive "invoicing"}} class="active"{{end}} href="{{.Period.InvoicingHref}}">Invoicing</a>{{end}}
    {{if .ShowInfo}}<a{{if .IsActive "info"}} class="active"{{end}} href="{{.Period.InfoHref}}">Info</a>{{end}}
  </nav>
  {{end}}
  <div class="hdr-right">
    <span class="avatar" title="{{.Login}}">
      <span class="avatar-initials">{{.Initials}}</span>
      {{with .AvatarURL}}<img src="{{.}}" alt="" referrerpolicy="no-referrer" onerror="this.remove()">{{end}}
    </span>
    <form method="post" action="/auth/logout">
      <button class="icon-button" title="Log out" aria-label="Log out">` + logoutIcon + `</button>
    </form>
  </div>
</header>{{end}}
`

// logoutIcon is the usual door-with-an-arrow mark, inlined like every other
// icon here so the page needs no icon font or CDN.
const logoutIcon = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>`
