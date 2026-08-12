package webui

import (
	"strings"
	"testing"
	"text/template"
)

// TestNavOrder pins the menu order. Spending sits next to the dashboard it
// drills into, ahead of Info.
func TestNavOrder(t *testing.T) {
	h := Header{
		Login: "octocat", Active: PageSpending,
		ShowFinance: true, ShowSpending: true, ShowInvoicing: true, ShowInfo: true,
		SpendingURL: "/2026/8/spending",
	}
	tmpl := template.Must(template.New("t").Parse(HeaderTemplate + `{{template "sitehead" .}}`))
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
