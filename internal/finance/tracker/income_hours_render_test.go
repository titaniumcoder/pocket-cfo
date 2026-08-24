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
		SpendableLabel:        "November 2026",
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
	f.computeTotal(0, 0, 176, true, 7500)
	if f.TotalHours != "176" {
		t.Errorf("TotalHours for whole hours = %q, want %q", f.TotalHours, "176")
	}

	f = Figures{}
	f.computeTotal(8.5, 63750, 160, true, 7500)
	if f.TotalHours != "168:30" {
		t.Errorf("TotalHours for fractional hours = %q, want %q", f.TotalHours, "168:30")
	}
}
