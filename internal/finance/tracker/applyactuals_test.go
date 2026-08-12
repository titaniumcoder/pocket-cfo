package tracker

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestActualStatus(t *testing.T) {
	tests := []struct {
		name       string
		row        CategoryRow
		complete   bool
		viewed     time.Month
		charged    map[string][]time.Month
		wantStatus string
	}{
		{
			name: "no actual, nothing to say",
			row:  CategoryRow{CategoryID: "a", PlannedCents: 40000},
		},
		{
			name:       "over plan fires immediately, regardless of coverage",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 36600, HasActual: true},
			wantStatus: ActualOver,
		},
		{
			name:       "under plan is withheld until the month is fully read",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 20000, HasActual: true},
			wantStatus: "",
		},
		{
			name:       "under plan once coverage is complete",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 20000, HasActual: true},
			complete:   true,
			wantStatus: ActualUnder,
		},
		{
			name:       "exactly on plan is not under",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 35000, ActualCents: 35000, HasActual: true},
			complete:   true,
			wantStatus: "",
		},
		{
			name:       "charged against a category planned at zero",
			row:        CategoryRow{CategoryID: "a", PlannedCents: 0, ActualCents: 4000, HasActual: true},
			complete:   true,
			wantStatus: ActualUnbudgeted,
		},
		{
			name:       "a one-off charged in the wrong month outranks over",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 0, ActualCents: 180000, HasActual: true},
			viewed:     time.August,
			charged:    map[string][]time.Month{"laptop": {time.August}},
			wantStatus: ActualMistimed,
		},
		{
			name:       "a one-off due now but already charged elsewhere",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 180000},
			viewed:     time.October,
			charged:    map[string][]time.Month{"laptop": {time.August}},
			wantStatus: ActualMistimed,
		},
		{
			name:       "a one-off charged in its own month is fine",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 180000, ActualCents: 180000, HasActual: true},
			viewed:     time.October,
			charged:    map[string][]time.Month{"laptop": {time.October}},
			wantStatus: "",
		},
		{
			name:       "a recurring category can never be mistimed",
			row:        CategoryRow{CategoryID: "rent", PlannedCents: 90000, ActualCents: 90000, HasActual: true},
			viewed:     time.August,
			charged:    map[string][]time.Month{"rent": {time.July, time.August}},
			wantStatus: "",
		},
		{
			name:       "year view passes no charged map, so nothing is mistimed",
			row:        CategoryRow{CategoryID: "laptop", PlannedDate: "2026-10-01", PlannedCents: 0, ActualCents: 180000, HasActual: true},
			viewed:     time.August,
			wantStatus: ActualUnbudgeted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := actualStatus(tt.row, tt.complete, tt.viewed, tt.charged)
			if got != tt.wantStatus {
				t.Errorf("actualStatus = %q, want %q", got, tt.wantStatus)
			}
		})
	}
}

func TestApplyActualsFillsWithoutTouchingThePlan(t *testing.T) {
	bv := BudgetView{
		Groups: []CategoryGroupView{{
			Name:         "Food",
			PlannedCents: 55000,
			Rows: []CategoryRow{
				{Name: "Groceries", CategoryID: "food.groceries", PlannedCents: 35000},
				{Name: "Restaurants", CategoryID: "food.restaurants", PlannedCents: 20000},
			},
		}},
		TotalPlannedCents: 55000,
	}
	av := ActualsView{Present: true, Complete: true, ByCategory: map[string]int{"food.groceries": 36600}, TotalCents: 36600}

	ApplyActuals(&bv, av, time.August, nil)

	if bv.TotalPlannedCents != 55000 || bv.Groups[0].PlannedCents != 55000 {
		t.Error("ApplyActuals changed a planned total — actuals must be display-only")
	}
	if bv.Groups[0].Rows[0].PlannedCents != 35000 || bv.Groups[0].Rows[1].PlannedCents != 20000 {
		t.Error("ApplyActuals changed a row's planned figure")
	}
	if got := bv.Groups[0].Rows[0].ActualCents; got != 36600 {
		t.Errorf("Groceries ActualCents = %d, want 36600", got)
	}
	if bv.Groups[0].Rows[1].HasActual {
		t.Error("Restaurants has no recorded spending and must not be marked as having any")
	}
	if got := bv.Groups[0].ActualCents; got != 36600 {
		t.Errorf("group ActualCents = %d, want the sum of its rows", got)
	}
}

func TestApplyActualsDoesNothingWithoutAFile(t *testing.T) {
	bv := BudgetView{Groups: []CategoryGroupView{{Rows: []CategoryRow{{CategoryID: "a", PlannedCents: 100}}}}}
	ApplyActuals(&bv, ActualsView{}, time.August, nil)
	if bv.Groups[0].Rows[0].HasActual || bv.Groups[0].Rows[0].ActualStatus != "" {
		t.Error("a period with no imported file must be left completely untouched")
	}
}

func TestUnmatchedCentsSplitsByKind(t *testing.T) {
	bv := BudgetView{
		Groups:        []CategoryGroupView{{Rows: []CategoryRow{{CategoryID: "food.groceries", HasActual: true}}}},
		CompanyGroups: []CategoryGroupView{{Rows: []CategoryRow{{CategoryID: "co.office", HasActual: true}}}},
	}
	av := ActualsView{Present: true, ByCategory: map[string]int{
		"food.groceries": 100, // matched
		"co.office":      200, // matched
		"co.gone":        300, // company-kind, no row
		"gone.entirely":  400, // not in budget.json at all
	}}
	private, company := UnmatchedCents(bv, av, map[string]bool{"co.office": true, "co.gone": true})
	if company != 300 {
		t.Errorf("company unmatched = %d, want 300", company)
	}
	if private != 400 {
		t.Errorf("private unmatched = %d, want 400 (an unknown id has no kind and falls to private)", private)
	}
}

// actualsTracker builds a tracker over the shared test budget plus an
// optional actuals file.
func actualsTracker(t *testing.T, actuals map[string]string) *Tracker {
	t.Helper()
	trk := accountsTracker(t, testAccountsJSON)
	if actuals != nil {
		trk.Actuals = newTestActuals(t, actuals)
	}
	return trk
}

// TestActualsChangeNoPlannedFigure: the layer is display-only, so every
// planned-based figure must be bit-identical with and without it.
func TestActualsChangeNoPlannedFigure(t *testing.T) {
	ctx := context.Background()
	month := time.August

	without := actualsTracker(t, nil).ComputeMonth(ctx, 2026, month)
	with := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"x1","date":"2026-08-03","description":"LIDL","amount":1000,"account":"A","category":"rent"}]}`,
	}).ComputeMonth(ctx, 2026, month)

	if !with.ShowActuals {
		t.Fatal("the actuals layer did not switch on — the rest of this test would prove nothing")
	}
	if with.BalanceCents != without.BalanceCents {
		t.Errorf("BalanceCents = %d with actuals, %d without", with.BalanceCents, without.BalanceCents)
	}
	if with.AvailableCents != without.AvailableCents {
		t.Errorf("AvailableCents = %d with actuals, %d without", with.AvailableCents, without.AvailableCents)
	}
	if with.OpeningBalanceCents != without.OpeningBalanceCents {
		t.Errorf("OpeningBalanceCents = %d with actuals, %d without", with.OpeningBalanceCents, without.OpeningBalanceCents)
	}
	if with.PrivateTotalPlannedCents != without.PrivateTotalPlannedCents {
		t.Errorf("PrivateTotalPlannedCents = %d with actuals, %d without", with.PrivateTotalPlannedCents, without.PrivateTotalPlannedCents)
	}
	if len(with.PrivateGroups) != len(without.PrivateGroups) {
		t.Fatalf("group count changed: %d vs %d", len(with.PrivateGroups), len(without.PrivateGroups))
	}
	for i := range with.PrivateGroups {
		if with.PrivateGroups[i].PlannedCents != without.PrivateGroups[i].PlannedCents {
			t.Errorf("group %q planned = %d with actuals, %d without",
				with.PrivateGroups[i].Name, with.PrivateGroups[i].PlannedCents, without.PrivateGroups[i].PlannedCents)
		}
		for j := range with.PrivateGroups[i].Rows {
			a, b := with.PrivateGroups[i].Rows[j], without.PrivateGroups[i].Rows[j]
			if a.PlannedCents != b.PlannedCents {
				t.Errorf("row %q planned = %d with actuals, %d without", a.Name, a.PlannedCents, b.PlannedCents)
			}
		}
	}
}

// TestNoActualsRendersByteIdentically: a month with no imported file must
// produce exactly the HTML it produced before this layer existed.
func TestNoActualsRendersByteIdentically(t *testing.T) {
	ctx := context.Background()

	unconfigured := actualsTracker(t, nil).ComputeMonth(ctx, 2026, time.August)
	empty := actualsTracker(t, map[string]string{}).ComputeMonth(ctx, 2026, time.August)

	var a, b strings.Builder
	recA, recB := httptest.NewRecorder(), httptest.NewRecorder()
	RenderPage(recA, unconfigured)
	RenderPage(recB, empty)
	a.Write(recA.Body.Bytes())
	b.Write(recB.Body.Bytes())

	if a.String() != b.String() {
		t.Error("a month with no actuals file rendered differently from one with the layer switched off entirely")
	}
	for _, marker := range []string{"with-actuals", "colhead", "Actually spent", "mistimed-note"} {
		if strings.Contains(a.String(), marker) {
			t.Errorf("the no-actuals page contains %q — nothing may be shown when there is nothing to show", marker)
		}
	}
}

func TestActualsErrorDegradesTheSectionNotThePage(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[],"transactions":[`, // malformed
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if f.ActualsErr == "" {
		t.Error("a broken actuals file must set ActualsErr")
	}
	if f.ShowActuals {
		t.Error("a broken actuals file must leave the layer off")
	}
	if f.BalanceCents == 0 && f.PrivateTotalPlannedCents == 0 {
		t.Error("the rest of the page should still compute")
	}
}

func TestActualsYearViewOnlyForPastYears(t *testing.T) {
	files := map[string]string{}
	for _, m := range []string{"01", "02"} {
		files["actuals/2026-"+m+".json"] = `{"month":"2026-` + m + `","coverage":[{"account":"A","from":"2026-` + m + `-01","to":"2026-` + m + `-28","imported_at":"2026-03-01"}],
			"transactions":[{"id":"y` + m + `","date":"2026-` + m + `-05","description":"X","amount":100,"account":"A","category":"rent"}]}`
	}
	trk := actualsTracker(t, files)

	// testNow is July 2026, so 2026 is the current year: ForYear projects
	// private spend forward from now, which actuals can't be compared with.
	current := trk.ComputeYear(context.Background(), 2026)
	if current.ShowActuals {
		t.Error("the current year must not show actuals — its planned figures are a forward projection")
	}
}

// TestDescriptionsNeverReachTheDashboard: computeActuals reads only
// ByCategory and TotalCents, so a description isn't in the struct the
// dashboard renders. That, not the 403, is what makes a leak impossible.
func TestDescriptionsNeverReachTheDashboard(t *testing.T) {
	const secret = "VERY-PRIVATE-MERCHANT-NAME"
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[
				{"id":"s1","date":"2026-08-03","description":"` + secret + `","amount":1000,"account":"A","category":"rent"},
				{"id":"s2","date":"2026-08-04","description":"` + secret + `-IGNORED","amount":-50,"account":"A","ignored":"refund of something"}
			]}`,
	})

	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if !f.ShowActuals {
		t.Fatal("the actuals layer did not switch on — this test would prove nothing")
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("a transaction description reached the dashboard HTML")
	}

	// And it is reachable through the admin path, so the check above isn't
	// passing merely because the data never loaded.
	sv := trk.ComputeSpending(context.Background(), 2026, time.August)
	if !sv.Present {
		t.Fatal("ComputeSpending found nothing — the fixture never loaded")
	}
	recS := httptest.NewRecorder()
	RenderSpending(recS, sv)
	if !strings.Contains(recS.Body.String(), secret) {
		t.Error("the admin drill-down should show descriptions; it showed none")
	}
	if !strings.Contains(recS.Body.String(), "refund of something") {
		t.Error("an ignored line must appear with its reason, so the page reconciles to the statement")
	}
}

// TestGroupHeaderColumnsMatchItsRows pins the alignment that shipped wrong:
// the header used to be a flex row with actual on the left and planned on the
// right — the reverse of both the Planned/Actual column header and the rows
// beneath it, and lining up with neither.
func TestGroupHeaderColumnsMatchItsRows(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"x1","date":"2026-08-03","description":"LIDL","amount":1200,"account":"A","category":"rent"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	if !f.ShowActuals {
		t.Fatal("the actuals layer did not switch on")
	}

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	header := regexp.MustCompile(`(?s)<div class="group-header"[^>]*>(.*?)</div>`).FindStringSubmatch(body)
	if header == nil {
		t.Fatal("no group header rendered")
	}
	cells := regexp.MustCompile(`<span class="(mid|amt)[^"]*">`).FindAllStringSubmatch(header[1], -1)
	if len(cells) != 2 || cells[0][1] != "mid" || cells[1][1] != "amt" {
		t.Fatalf("group header cells = %v, want mid then amt so they align with the rows", cells)
	}

	// The header's planned figure must be in .mid, like every row's.
	mid := regexp.MustCompile(`(?s)<span class="mid">(.*?)</span>`).FindStringSubmatch(header[1])
	if mid == nil || strings.Contains(mid[1], "&minus;") {
		t.Errorf("header .mid = %q, want the planned figure without a minus, matching the rows", mid)
	}

	// And the column header exists on both ledgers, so the two numbers are labelled.
	if got := strings.Count(body, `class="row colhead"`); got != 2 {
		t.Errorf("found %d column headers, want one per ledger", got)
	}
}
