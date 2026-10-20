package tracker

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderPageExpectedRowsIncludeHourUnit(t *testing.T) {
	f := Figures{
		LastUpdated:           "—",
		Mode:                  "month",
		Currency:              "€",
		ShowExpected:          true,
		ExpectedRange:         "01.09. - 30.09.26",
		ExpectedHours:         "176",
		ExpectedRate:          "75",
		ExpectedCents:         1320000,
		ShowVacation:          true,
		VacationHoursDeducted: "24",
		VacationCentsDeducted: 180000,
		ExpectedNetHours:      "152",
		ExpectedNetCents:      1140000,
		TotalHours:            "152",
		TotalRate:             "75",
		TotalCents:            1140000,
		Personal:              PersonalView{},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)

	body := rec.Body.String()

	// Expected row mid column must include 'h'
	if !strings.Contains(body, `<span class="mid">176 h &times; 75</span>`) {
		t.Errorf("Expected row missing 'h' in calculation mid span; body:\n%s", body)
	}

	// Vacation row mid column must include 'h'
	if !strings.Contains(body, `<span class="mid">24 h &times; 75</span>`) {
		t.Errorf("Vacation row missing 'h' in calculation mid span; body:\n%s", body)
	}

	// Expected total row mid column must include 'h'
	if !strings.Contains(body, `<span class="mid">152 h &times; 75</span>`) {
		t.Errorf("Expected total row missing 'h' in calculation mid span; body:\n%s", body)
	}

	// Total (Income) row mid column must include 'h' and compact hours
	if !strings.Contains(body, `<span class="mid">152 h &times; 75</span>`) {
		t.Errorf("Total row missing 'h' or compact format in calculation mid span; body:\n%s", body)
	}
}

func TestComputeTotalFormatsHoursCompactly(t *testing.T) {
	var f Figures
	f.computeTotal(0, 0, 176, true, 7500, 0, 0)
	if f.TotalHours != "176" {
		t.Errorf("TotalHours for whole hours = %q, want %q", f.TotalHours, "176")
	}

	f = Figures{}
	f.computeTotal(8.5, 63750, 160, true, 7500, 0, 0)
	if f.TotalHours != "168:30" {
		t.Errorf("TotalHours for fractional hours = %q, want %q", f.TotalHours, "168:30")
	}
}

// TestTheIncomePanelListsInvoicedTrackedAndExpected: the income panel reads
// top to bottom as billing state — the covered rows strike out under
// Invoiced, the rest stays under Tracked, and Expected holds the projection
// plus the estimated invoices whose income month the page covers.
func TestTheIncomePanelListsInvoicedTrackedAndExpected(t *testing.T) {
	f := Figures{
		Mode:     "month",
		Currency: "€",
		Covered: []TrackedRow{
			{Project: "WMC Sinterstar", Hours: "8:00", Rate: "75", AmountCents: 600000, Note: "until 10.10."},
		},
		Tracked: []TrackedRow{
			{Project: "Sowemo", Hours: "2:00", Rate: "75", AmountCents: 150000, Note: "from 11.10."},
		},
		ShowExpected:     true,
		ExpectedRange:    "21.10. - 31.10.26",
		ExpectedHours:    "80",
		ExpectedRate:     "75",
		ExpectedCents:    600000,
		ExpectedNetHours: "80",
		ExpectedNetCents: 600000,
		ExpectedEstimates: []InvoicedRow{
			{Estimated: true, WorkedMonth: "October 2026", AmountCents: 990000},
		},
		TotalHours: "90:00",
		TotalRate:  "75",
		TotalCents: 750000,
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	for _, want := range []string{"<h2>Invoiced</h2>", `class="row struck"`, "until 10.10.", "<h2>Tracked</h2>", "from 11.10.", "<h2>Expected</h2>", "estimate for October 2026"} {
		if !strings.Contains(body, want) {
			t.Errorf("the income panel is missing %q", want)
		}
	}
	invoiced := body[strings.Index(body, "<h2>Invoiced</h2>"):strings.Index(body, "<h2>Tracked</h2>")]
	if !strings.Contains(invoiced, "WMC Sinterstar") || strings.Contains(invoiced, "Sowemo") {
		t.Errorf("the Invoiced section holds the wrong rows:\n%s", invoiced)
	}
	expected := body[strings.Index(body, "<h2>Expected</h2>"):]
	if !strings.Contains(expected, "21.10.") || !strings.Contains(expected, "estimate for October 2026") {
		t.Errorf("the Expected section holds neither the projection nor the estimate:\n%s", expected)
	}
}

// TestAnEstimateRendersAsAnEstimateNotAnInvoice: an estimated invoice shows
// as the grey estimate for its worked month with no PDF link, while a real
// invoice keeps its number and its link.
func TestAnEstimateRendersAsAnEstimateNotAnInvoice(t *testing.T) {
	f := Figures{
		Mode:     "month",
		Currency: "€",
		Tracked: []TrackedRow{
			{Project: "Sowemo", Hours: "2 h", Rate: "75", AmountCents: 150000, Unlinked: true},
			{Project: "Internal", Hours: "1 h", Rate: "75", AmountCents: 75000, Ignored: true},
		},
		Invoiced: []InvoicedRow{
			{Number: "INV-0000000001", Day: "30.10.", AmountCents: 500000, URL: "/invoicing/invoices/INV-0000000001.pdf"},
			{Estimated: true, WorkedMonth: "October 2026", AmountCents: 990000},
		},
		TotalHours: "176",
		TotalRate:  "75",
		TotalCents: 1140000,
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	if !strings.Contains(body, "estimate for October 2026") {
		t.Errorf("the estimate row is missing its label; body:\n%s", body)
	}
	if !strings.Contains(body, "no recipient linked") || !strings.Contains(body, "not invoiced") {
		t.Error("the tracked rows are missing the unlinked/ignored markers")
	}
	estimate := body[strings.Index(body, "estimate for October 2026"):]
	estimate = estimate[:strings.Index(estimate, "</div>")]
	if strings.Contains(estimate, `href=`) {
		t.Errorf("the estimate row carries a link: %s", estimate)
	}
	if !strings.Contains(body, `href="/invoicing/invoices/INV-0000000001.pdf"`) {
		t.Error("the real invoice row lost its PDF link")
	}
}
