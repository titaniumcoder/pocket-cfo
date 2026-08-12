package webui

import (
	htmltemplate "html/template"
	"strings"
	"testing"
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
