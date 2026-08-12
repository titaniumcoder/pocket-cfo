package tracker

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChangeRequestCarriesTheExactAmount(t *testing.T) {
	tx := SpendingTx{ID: "b0442e17", Date: "2026-08-03", Description: "LIDL SOFIA 4412", Cents: 21040}
	want := "Change ID b0442e17 (2026-08-03 / LIDL SOFIA 4412 / 210.40) like this: "
	if got := tx.ChangeRequest(); got != want {
		t.Errorf("ChangeRequest() = %q, want %q", got, want)
	}
	// Rounding to whole euros would match the wrong statement line.
	if strings.Contains(tx.ChangeRequest(), "/ 210)") {
		t.Error("the amount was rounded")
	}
}

// TestSpendingPageIsQuiet pins what the screen deliberately does not say: the
// coverage caveat lives in the Coverage table, not in a banner, and each
// category footer is one line rather than three.
func TestSpendingPageIsQuiet(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-09","imported_at":"2026-08-10"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)
	if !v.Present {
		t.Fatal("the fixture did not load")
	}

	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	body := rec.Body.String()

	if strings.Contains(body, "Reconciled through") {
		t.Error("the coverage banner is back; the Coverage table says it in a constant layout")
	}
	if !strings.Contains(body, "<h3>Coverage</h3>") {
		t.Error("the Coverage table is missing")
	}
	for _, gone := range []string{">Variance<", ">Planned<"} {
		if strings.Contains(body, gone) {
			t.Errorf("%s row is back; the footer is one line", gone)
		}
	}
	if !strings.Contains(body, "(Budget:") {
		t.Error("the footer does not carry the budget")
	}
	if !strings.Contains(body, `class="copy-tx"`) || !strings.Contains(body, "Change ID t1") {
		t.Error("the copy link is missing")
	}
}

// TestSpendingSteppsThroughMonths: the page is a month at a time, so it needs
// the same stepper the dashboard has, pointed at itself. A stepper that walked
// back to the dashboard would drop you out of the page every time you changed
// month.
func TestSpendingSteppsThroughMonths(t *testing.T) {
	trk := actualsTracker(t, map[string]string{})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)

	if v.Nav.PrevURL != "/2026/7/spending" || v.Nav.NextURL != "/2026/9/spending" {
		t.Errorf("prev/next = %q/%q, want the neighbouring months of this page", v.Nav.PrevURL, v.Nav.NextURL)
	}
	if !strings.HasSuffix(v.Nav.TodayURL, "/spending") {
		t.Errorf("TodayURL = %q, want a spending month", v.Nav.TodayURL)
	}
	if v.Nav.Year != 2026 || v.Nav.MonthNum != 8 || len(v.Nav.Months) != 12 {
		t.Errorf("selects = %d/%d over %d months, want August 2026 over 12", v.Nav.MonthNum, v.Nav.Year, len(v.Nav.Months))
	}
	if v.OverviewURL != "/2026/8" {
		t.Errorf("OverviewURL = %q, want the same month's dashboard", v.OverviewURL)
	}

	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	body := rec.Body.String()
	for _, want := range []string{`class="periodnav`, `href="/2026/7/spending"`, `href="/2026/8"`, ">Overview<", ">Reload<"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %s — it should carry the same chrome as every other page", want)
		}
	}
}

// TestSpendingNavBoundsMatchTheDashboard: the two pages walk the same months,
// so they must stop at the same edges. Sharing monthNav is what guarantees it;
// this fails if either page grows its own copy.
func TestSpendingNavBoundsMatchTheDashboard(t *testing.T) {
	trk := actualsTracker(t, map[string]string{})
	now := time.Now().In(trk.Loc)
	for _, year := range []int{now.Year() - yearRangeForTest, now.Year() + yearRangeForTest} {
		for _, month := range []time.Month{time.January, time.December} {
			v := trk.ComputeSpending(context.Background(), year, month)
			f := trk.ComputeMonth(context.Background(), year, month)
			if v.Nav.PrevDisabled != f.PrevDisabled || v.Nav.NextDisabled != f.NextDisabled {
				t.Errorf("%s %d: spending arrows %v/%v, dashboard %v/%v",
					month, year, v.Nav.PrevDisabled, v.Nav.NextDisabled, f.PrevDisabled, f.NextDisabled)
			}
		}
	}
}

const yearRangeForTest = 2

// TestDashboardHasNoCoverageBanner: reconciliation belongs on the spending
// screen only.
func TestDashboardHasNoCoverageBanner(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-09","imported_at":"2026-08-10"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if !f.ShowActuals {
		t.Fatal("the actuals layer did not switch on")
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	if strings.Contains(rec.Body.String(), "Reconciled through") {
		t.Error("the dashboard carries a coverage banner; it belongs on the spending screen")
	}
}
