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
