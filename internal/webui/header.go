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

const (
	PageFinance   = "finance"
	PageSpending  = "spending"
	PageInvoicing = "invoicing"
	PageInfo      = "info"
)

type Header struct {
	Login  string
	Active string

	ShowFinance   bool
	ShowSpending  bool
	ShowInvoicing bool
	ShowInfo      bool

	Period Period
}

type Period struct {
	Year     int
	Month    int
	YearView bool
}

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

func (p Period) SpendingHref() string {
	if p.Year == 0 || p.Month == 0 {
		return ""
	}
	return fmt.Sprintf("/%d/%d/spending", p.Year, p.Month)
}

func (p Period) InvoicingHref() string { return "/invoicing" + p.Query() }

func (p Period) InfoHref() string { return "/info" + p.Query() }

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

func (h Header) Initials() string {
	login := strings.TrimSpace(h.Login)
	if login == "" {
		return "?"
	}
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

func (h Header) HasNav() bool {
	n := 0
	for _, shown := range []bool{h.ShowFinance, h.ShowSpending, h.ShowInvoicing, h.ShowInfo} {
		if shown {
			n++
		}
	}
	return n > 1
}

func (h Header) IsActive(page string) bool { return h.Active == page }

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

const logoutIcon = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>`
