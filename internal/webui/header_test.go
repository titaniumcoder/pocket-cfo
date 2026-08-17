package webui

import (
	htmltemplate "html/template"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/buildinfo"
)

// TestNavOrder pins the menu order. Spending sits next to the dashboard it
// drills into, ahead of Info.
func TestNavOrder(t *testing.T) {
	h := Header{
		Login: "octocat", Active: PageSpending,
		ShowFinance: true, ShowSpending: true, ShowInvoicing: true, ShowInfo: true,
		Period: Period{Year: 2026, Month: 8},
	}
	tmpl := htmltemplate.Must(htmltemplate.New("t").Parse(HeaderTemplate + `{{template "sitehead" .}}`))
	var b strings.Builder
	if err := tmpl.Execute(&b, h); err != nil {
		t.Fatal(err)
	}
	body := b.String()

	var order []int
	for _, label := range []string{">Finance<", ">Spending<", ">Invoicing<", ">Info<"} {
		i := strings.Index(body, label)
		if i < 0 {
			t.Fatalf("%s is missing from the menu", label)
		}
		order = append(order, i)
	}
	for i := 1; i < len(order); i++ {
		if order[i] < order[i-1] {
			t.Fatalf("menu order is wrong: want Finance, Spending, Invoicing, Info")
		}
	}
	if !strings.Contains(body, `class="active" href="/2026/8/spending"`) {
		t.Error("the Spending entry should be marked current and carry the viewed month")
	}
	// Every entry carries the month, not just the one that has it in its path.
	for _, want := range []string{`href="/2026/8"`, `href="/invoicing?year=2026&amp;month=8"`, `href="/info?year=2026&amp;month=8"`} {
		if !strings.Contains(body, want) {
			t.Errorf("menu link %s is missing — changing page should not change the month", want)
		}
	}
}

// TestPeriodRoundTrip: a page with no month of its own carries the one it was
// reached with, so Finance -> Info -> Finance lands where it started.
func TestPeriodRoundTrip(t *testing.T) {
	for _, p := range []Period{{Year: 2026, Month: 8}, {Year: 2026, Month: 1, YearView: true}} {
		q := strings.TrimPrefix(p.Query(), "?")
		vals := map[string]string{}
		for _, kv := range strings.Split(q, "&") {
			k, v, _ := strings.Cut(kv, "=")
			vals[k] = v
		}
		if got := ParsePeriod(vals["year"], vals["month"]); got != p {
			t.Errorf("round trip of %+v produced %+v", p, got)
		}
	}
}

// TestPeriodWithoutAMonthStaysAYear: a year view hands on the year only, so
// Finance goes back to the year view rather than silently picking a month.
func TestPeriodWithoutAMonthStaysAYear(t *testing.T) {
	p := Period{Year: 2025, Month: 1, YearView: true}
	if got := p.FinanceHref(); got != "/2025" {
		t.Errorf("FinanceHref() = %q, want /2025", got)
	}
	// Spending is a month at a time even so, or the entry would be dead.
	if got := p.SpendingHref(); got != "/2025/1/spending" {
		t.Errorf("SpendingHref() = %q, want a real month", got)
	}
}

// TestZeroPeriodIsTodayShaped: a page reached with no period at all links the
// way it did before periods existed, rather than to /0/0.
func TestZeroPeriodIsTodayShaped(t *testing.T) {
	var p Period
	if p.FinanceHref() != "/" || p.InvoicingHref() != "/invoicing" || p.InfoHref() != "/info" {
		t.Errorf("zero period produced %q %q %q", p.FinanceHref(), p.InvoicingHref(), p.InfoHref())
	}
}

// TestHasNavCountsSpending: a session that can reach only the dashboard and
// the spending page still has somewhere to go, so it gets a menu.
func TestHasNavCountsSpending(t *testing.T) {
	if !(Header{ShowFinance: true, ShowSpending: true}).HasNav() {
		t.Error("two reachable pages should produce a menu")
	}
	if (Header{ShowSpending: true}).HasNav() {
		t.Error("one reachable page is not navigation")
	}
}

// TestAvatarFallsBackWhenTheImageFails: the initials sit behind the picture so
// a missing one degrades to something readable — but that only worked when
// there was no URL at all. A URL that fails to load left the browser painting
// a broken-image glyph over the initials, which is what the header actually
// showed.
func TestAvatarFallsBackWhenTheImageFails(t *testing.T) {
	tmpl := htmltemplate.Must(htmltemplate.New("t").Parse(HeaderTemplate + `{{template "sitehead" .}}`))
	var b strings.Builder
	if err := tmpl.Execute(&b, Header{Login: "octocat"}); err != nil {
		t.Fatal(err)
	}
	body := b.String()

	if !strings.Contains(body, "onerror=") {
		t.Error("a failed avatar has nothing to fall back to; the broken-image icon stays")
	}
	// The initials must be in the markup regardless, since they are what is
	// left once the image removes itself.
	if !strings.Contains(body, `class="avatar-initials">Oc<`) {
		t.Errorf("no initials behind the picture: %s", body)
	}

	// And with no URL at all there is no image to fail.
	b.Reset()
	if err := tmpl.Execute(&b, Header{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "<img") {
		t.Error("an image is rendered for a session with no avatar")
	}
}

func renderHeader(t *testing.T, h Header) string {
	t.Helper()
	tmpl := htmltemplate.Must(htmltemplate.New("t").Parse(HeaderTemplate + `{{template "sitehead" .}}`))
	var b strings.Builder
	if err := tmpl.Execute(&b, h); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The version and the data stamp are read from the process, not carried on the
// Header, so they render the same whoever built it — including the two places
// that build one without knowing either exists.
func TestBrandShowsVersionAndDataStamp(t *testing.T) {
	defer restoreBuildInfo(t)()
	buildinfo.Version = "v9.9.9"
	buildinfo.Data = buildinfo.DataStamp{UpdatedAt: "2026-08-17", Commit: "a1b2c3d4e5"}

	body := renderHeader(t, Header{Login: "octocat"})
	for _, want := range []string{
		"Pocket CFO",
		`<span class="version">v9.9.9</span>`,
		`<div class="data-stamp">Last Data Update: 17.08.2026 - a1b2c3d</div>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the header does not contain %q:\n%s", want, body)
		}
	}
}

// Neither is required, and an absent one must leave no markup behind — an
// empty span or a stray "Last Data Update:" would be worse than nothing.
func TestBrandOmitsWhatItWasNotGiven(t *testing.T) {
	defer restoreBuildInfo(t)()
	buildinfo.Version = ""
	buildinfo.Data = buildinfo.DataStamp{}

	body := renderHeader(t, Header{Login: "octocat"})
	if !strings.Contains(body, "Pocket CFO") {
		t.Error("the header lost its title")
	}
	for _, unwanted := range []string{`class="version"`, "data-stamp", "Last Data Update"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("an unset value still rendered %q:\n%s", unwanted, body)
		}
	}
}

// The data stamp is supplied by the deployment and the version by the build,
// so one can be present without the other.
func TestBrandShowsAVersionWithNoDataStamp(t *testing.T) {
	defer restoreBuildInfo(t)()
	buildinfo.Version = "dev"
	buildinfo.Data = buildinfo.DataStamp{}

	body := renderHeader(t, Header{})
	if !strings.Contains(body, `<span class="version">dev</span>`) {
		t.Errorf("the version is missing:\n%s", body)
	}
	if strings.Contains(body, "data-stamp") {
		t.Errorf("an unset data stamp still rendered:\n%s", body)
	}
}

func restoreBuildInfo(t *testing.T) func() {
	t.Helper()
	version, data := buildinfo.Version, buildinfo.Data
	return func() { buildinfo.Version, buildinfo.Data = version, data }
}
