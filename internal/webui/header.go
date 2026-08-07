// Package webui holds the chrome shared by every PocketCFO dashboard page —
// currently the site header (brand, top menu, session controls). It exists
// because those pages are rendered by two different template systems: the
// finance dashboard from a Go-embedded template set
// (internal/finance/tracker), invoicing and info from files under
// templates/. Neither can reach the other's templates, so the shared markup
// lives here as a plain template source string that both parse, rather than
// being hand-copied into each page — a copy is exactly how the three pages
// drifted into three different headers in the first place.
//
// The matching CSS lives in static/app.css (.topnav/.hdr-right/header),
// including the header's own bottom spacing: the gap below the menu belongs
// to the menu, not to whatever each page happens to render underneath it.
package webui

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"strings"
)

// Page identifiers for Header.Active — which nav entry renders as current.
const (
	PageFinance   = "finance"
	PageInvoicing = "invoicing"
	PageInfo      = "info"
)

// Header is the data the shared site header renders from. The Show* flags
// are "this session may reach that page at all" — the caller derives them
// from the session (see cmd/pocketcfo), since only it knows the auth rules.
//
// The header is deliberately identical on every page: brand, the pages
// this session can reach, Logout, and who's signed in. Page-specific
// controls (the finance dashboard's period navigation and Reload) live with
// the content they act on, not up here — a header that changes shape
// between pages reads as a different app on each one.
//
// There is no read-only marker: every session that reaches these pages is
// read-only, so the badge told nobody anything.
type Header struct {
	Login  string
	Active string

	ShowFinance   bool
	ShowInvoicing bool
	ShowInfo      bool
}

// AvatarURL is where to fetch this user's picture. An email login gets its
// Gravatar; a GitHub login gets that account's avatar. Both are external
// requests — the only ones any page in this app makes — and the Gravatar
// URL necessarily discloses a hash of the address to gravatar.com. If that
// trade isn't wanted, returning "" here falls the header back to Initials
// below with no other change.
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

// Initials back the avatar: they render underneath the picture, so a login
// with no avatar (or an image that fails to load) still shows something
// recognisable rather than a broken-image icon.
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
	for _, shown := range []bool{h.ShowFinance, h.ShowInvoicing, h.ShowInfo} {
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
    {{if .ShowFinance}}<a{{if .IsActive "finance"}} class="active"{{end}} href="/">Finance</a>{{end}}
    {{if .ShowInvoicing}}<a{{if .IsActive "invoicing"}} class="active"{{end}} href="/invoicing">Invoicing</a>{{end}}
    {{if .ShowInfo}}<a{{if .IsActive "info"}} class="active"{{end}} href="/info">Info</a>{{end}}
  </nav>
  {{end}}
  <div class="hdr-right">
    <span class="avatar" title="{{.Login}}">
      <span class="avatar-initials">{{.Initials}}</span>
      {{with .AvatarURL}}<img src="{{.}}" alt="" referrerpolicy="no-referrer" loading="lazy">{{end}}
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
