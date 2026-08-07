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
  <nav class="topnav">
    {{if .HasNav}}
    {{if .ShowFinance}}<a{{if .IsActive "finance"}} class="active"{{end}} href="/">Finance</a>{{end}}
    {{if .ShowInvoicing}}<a{{if .IsActive "invoicing"}} class="active"{{end}} href="/invoicing">Invoicing</a>{{end}}
    {{if .ShowInfo}}<a{{if .IsActive "info"}} class="active"{{end}} href="/info">Info</a>{{end}}
    {{end}}
    <form method="post" action="/auth/logout"><button class="topnav-logout">Logout</button></form>
  </nav>
  <div class="hdr-right">
    <span class="login-email">{{.Login}}</span>
  </div>
</header>{{end}}
`
