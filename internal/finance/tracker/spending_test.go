package tracker

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

// TestCopyLinkWorksWithoutASecureContext: navigator.clipboard is absent over
// plain HTTP, and the link would then do nothing at all with no hint why.
func TestCopyLinkWorksWithoutASecureContext(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-09","imported_at":"2026-08-10"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	})
	rec := httptest.NewRecorder()
	RenderSpending(rec, trk.ComputeSpending(context.Background(), 2026, time.August))
	body := rec.Body.String()

	if !strings.Contains(body, "if (navigator.clipboard)") {
		t.Error("the copy link assumes a secure context")
	}
	for _, want := range []string{"execCommand", "flash(ok ? 'copied' : 'failed')"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s fallback; the link would fail silently over plain HTTP", want)
		}
	}

	// The control is an icon, and both states ship inside the link so the
	// confirmation swaps rather than rewrites — rewriting the contents is
	// what would leave an empty control behind.
	if strings.Contains(body, `title="Copy a change request for Hermes">copy</a>`) {
		t.Error("the copy control is still a word")
	}
	for _, want := range []string{`class="i-copy"`, `class="i-done"`, "classList.add(state)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the copy control is missing %s", want)
		}
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

// TestOffPlanIsWeightAndAMarkNotColour: colour on these pages means the sign
// of an amount. Using it for over/under budget as well left a red that could
// mean either, so a budget grade is bold plus one icon and nothing else.
func TestOffPlanIsWeightAndAMarkNotColour(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":2000,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	for _, gone := range []string{`class="amt over"`, `class="amt under"`, `class="amt unbudgeted"`, `class="amt mistimed"`} {
		if strings.Contains(body, gone) {
			t.Errorf("%s is back; a budget grade is not a colour", gone)
		}
	}
	if !strings.Contains(body, "flagged") {
		t.Error("nothing is marked off-plan, though the fixture overspends by a mile")
	}
	if !strings.Contains(body, "over budget</title>") {
		t.Error("the overspend carries no warning mark")
	}
}

// TestOnPlanSaysNothing is the point of the tolerance: two euros against a
// 1 000 budget is not news, and a page that flags it teaches you to ignore
// the flag that matters. Five percent of 1 000 is 50, so 1 002 is on plan.
func TestOnPlanSaysNothing(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":1002,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	if strings.Contains(body, "over budget</title>") {
		t.Error("two euros over a 1000 budget was flagged")
	}
	if strings.Contains(body, "flagged") {
		t.Error("a figure inside the tolerance was set in bold")
	}
}

// TestBudgetGradesAreNotColoured reads the stylesheet, because the template
// test above cannot: it can only prove the HTML stops naming a status, not
// that the mark and the bold text stay colourless. Colour here means the sign
// of an amount and nothing else.
func TestBudgetGradesAreNotColoured(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("..", "..", "..", "static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(css), "\n") {
		if !strings.Contains(line, ".flagged") && !strings.Contains(line, ".mark") {
			continue
		}
		if strings.Contains(line, "color:") {
			t.Errorf("a budget grade is coloured again: %s", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(string(css), ".flagged { font-weight: 700; }") {
		t.Error("off-plan is no longer set in bold")
	}
}

// TestExpenseLedgerReadsAsExpenses: every figure in the two expense ledgers is
// money leaving, so every one carries a minus — planned and actual, group
// header and row alike. Printing them unsigned put a budget of 1 420 next to a
// Net income of 4 715 in the same shape.
func TestExpenseLedgerReadsAsExpenses(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":1002,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	})
	f := trk.ComputeMonth(context.Background(), 2026, time.August)
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	// The signed figure and its colour travel together, in both columns.
	for _, want := range []string{`<span class="mid neg">&minus;`, `<span class="amt act neg`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in the ledger — an expense is not rendered as one", want)
		}
	}

	// One totals row per ledger, planned and actual side by side, rather than
	// a total followed by a second row that repeats the label.
	if strings.Contains(body, "Actually spent") {
		t.Error(`"Actually spent" is back; the total carries both figures in its own columns`)
	}
	for _, label := range []string{"Total company expenses", "Total private expenses"} {
		row := regexp.MustCompile(`(?s)<div class="row net neg"><span class="label">` +
			regexp.QuoteMeta(label) + `</span>(.*?)</div>`).FindStringSubmatch(body)
		if row == nil {
			t.Fatalf("no %s row", label)
		}
		if n := strings.Count(row[1], `class="mid`); n != 1 {
			t.Errorf("%s has %d planned cells, want 1", label, n)
		}
		// Signed unless it is zero, which is the rule outEuro implements:
		// nothing left, so there is no direction to show.
		if !strings.Contains(row[1], "&minus;") && !strings.Contains(row[1], ">0<") {
			t.Errorf("%s does not read as an expense: %s", label, row[1])
		}
		// The "of planned" is what the phone layout shows in place of .mid,
		// so a total without it loses its budget on a narrow screen.
		if !strings.Contains(row[1], `class="plan-m"`) {
			t.Errorf("%s carries no of-planned for the phone layout", label)
		}
	}
}

// TestPhoneLayoutCoversTheTotals: the narrow layout drops the planned column
// and shows "of 2,115" under the actual instead. That swap was scoped to the
// group rows, so the totals would have shown both at once — the column it was
// meant to replace, and the replacement.
func TestPhoneLayoutCoversTheTotals(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("..", "..", "..", "static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 600px\) \{(.*)`).FindStringSubmatch(string(css))
	if phone == nil {
		t.Fatal("no phone block in the stylesheet")
	}
	for _, want := range []string{
		".ledger.with-actuals .row.net .mid",
		".ledger.with-actuals .row.net .amt",
	} {
		if !strings.Contains(phone[1], want) {
			t.Errorf("the phone layout does not cover %s, so a total shows its budget twice", want)
		}
	}
}

const splitMonth = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
	"transactions":[
	  {"id":"w1","date":"2026-08-14","description":"ATM WITHDRAWAL","amount":100,"account":"A","splits":[
	    {"amount":60,"category":"00000000-0000-4000-8000-000000000001"},
	    {"amount":40,"ignored":"cash still in my pocket"}]}]}`

// TestSplitCountsOncePerPart is the whole risk in one test: a split must put
// each part's money against its own category and the line's own amount
// nowhere, or the month silently gains or loses the difference.
func TestSplitCountsOncePerPart(t *testing.T) {
	trk := actualsTracker(t, map[string]string{"actuals/2026-08.json": splitMonth})
	av, err := trk.Actuals.ForMonth(context.Background(), 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	// 60 against Rent, 40 ignored, and the 100 counted nowhere.
	if got := av.ByCategory["00000000-0000-4000-8000-000000000001"]; got != 6000 {
		t.Errorf("category total = %d cents, want 6000 — the part, not the line", got)
	}
	if av.TotalCents != 6000 {
		t.Errorf("month total = %d cents, want 6000: the ignored part is out and the line is not double-counted", av.TotalCents)
	}
}

// TestSplitAppearsUnderEachPart: the spending page reconciles line for line,
// so a split has to show up in both places it belongs, each saying what it is
// a part of.
func TestSplitAppearsUnderEachPart(t *testing.T) {
	trk := actualsTracker(t, map[string]string{"actuals/2026-08.json": splitMonth})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)
	if !v.Present {
		t.Fatal("the fixture did not load")
	}

	var spent, ignored *SpendingTx
	for gi := range v.Groups {
		for ci := range v.Groups[gi].Categories {
			for ti := range v.Groups[gi].Categories[ci].Transactions {
				if tx := &v.Groups[gi].Categories[ci].Transactions[ti]; tx.ID == "w1" {
					spent = tx
				}
			}
		}
	}
	for i := range v.Ignored {
		if v.Ignored[i].ID == "w1" {
			ignored = &v.Ignored[i]
		}
	}
	if spent == nil || ignored == nil {
		t.Fatalf("the split is missing a side: spent=%v ignored=%v", spent, ignored)
	}
	if spent.Cents != 6000 || ignored.Cents != 4000 {
		t.Errorf("parts are %d and %d cents, want 6000 and 4000", spent.Cents, ignored.Cents)
	}
	if spent.PartOf != "100" || ignored.PartOf != "100" {
		t.Errorf("parts do not name the line they came from: %q / %q", spent.PartOf, ignored.PartOf)
	}
	if !strings.Contains(spent.ChangeRequest(), "60.00 of 100") {
		t.Errorf("the copy text does not identify the part: %q", spent.ChangeRequest())
	}

	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	if n := strings.Count(rec.Body.String(), `class="part-of"`); n != 2 {
		t.Errorf("found %d part-of notes, want one per part", n)
	}
}

// topLevelCells counts the direct children of one grid row, ignoring spans
// nested inside a cell (the part-of note, an icon's title).
func topLevelCells(row string) int {
	depth, cells := 0, 0
	for i := 0; i < len(row); i++ {
		switch {
		case strings.HasPrefix(row[i:], "<span") || strings.HasPrefix(row[i:], "<svg"):
			if depth == 0 {
				cells++
			}
			depth++
		case strings.HasPrefix(row[i:], "</span>") || strings.HasPrefix(row[i:], "</svg>"):
			depth--
		}
	}
	return cells
}

// TestSpendingIsOneGrid is the fix for columns that never lined up: a table
// per category sized its columns from its own contents, so a short category
// put Description at one x and the next put it somewhere else. One grid makes
// that impossible rather than unlikely — a single set of tracks, and every row
// display:contents inside it.
func TestSpendingIsOneGrid(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"Revolut Private","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[
			  {"id":"t1","date":"2026-08-03","description":"L","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000001"},
			  {"id":"t2","date":"2026-08-04","description":"A much longer statement line than the other one","amount":15,"account":"Revolut Private","category":"00000000-0000-4000-8000-000000000002"},
			  {"id":"t3","date":"2026-08-05","description":"X","amount":9,"account":"A","ignored":"own account"}]}`,
	})
	rec := httptest.NewRecorder()
	RenderSpending(rec, trk.ComputeSpending(context.Background(), 2026, time.August))
	body := rec.Body.String()

	if n := strings.Count(body, `class="spend-grid"`); n != 1 {
		t.Fatalf("found %d transaction grids, want exactly 1 — two grids can drift apart", n)
	}
	if strings.Contains(body, "<table") {
		t.Error("a table is back on the spending page; its columns size themselves independently")
	}

	rows := regexp.MustCompile(`(?s)<div class="sg-row[^"]*">(.*?)</div>`).FindAllStringSubmatch(body, -1)
	if len(rows) < 4 {
		t.Fatalf("found %d rows, want the transactions and the category totals", len(rows))
	}
	for i, r := range rows {
		if got := topLevelCells(r[1]); got != 5 {
			t.Errorf("row %d contributes %d cells, want 5 — a row with a different count shifts every column after it", i, got)
		}
	}

	// Headings span the whole grid rather than landing in the first column.
	for _, want := range []string{`<h3 class="sg-span">`, `class="sg-span sg-cat"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s; a heading in one track would squeeze the others", want)
		}
	}
}

// TestGridTracksAreDeclaredNotComputed: the columns are the same everywhere
// because the stylesheet says so once, not because the contents happen to
// agree. Only Description flexes; the rest are content-width, which is what
// puts every amount on the same right edge down the whole page.
func TestGridTracksAreDeclaredNotComputed(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("..", "..", "..", "static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	// Anchored on display:grid so this reads the desktop rule rather than the
	// phone override, which sits earlier in the file and declares fewer tracks
	// on purpose.
	m := regexp.MustCompile(`\.spend-grid \{ display: grid; grid-template-columns: ([^;]+);`).FindStringSubmatch(string(css))
	if m == nil {
		t.Fatal("the spending grid declares no columns")
	}
	if n := strings.Count(m[1], "max-content") + strings.Count(m[1], "minmax"); n != 5 {
		t.Errorf("grid-template-columns = %q, want five declared tracks", m[1])
	}
	if strings.Count(m[1], "1fr") != 1 {
		t.Errorf("grid-template-columns = %q, want exactly one flexible track so the amounts share a right edge", m[1])
	}

	// The phone drops the two .col-secondary columns, so it must declare
	// three tracks — five would leave two empty ones eating the width.
	phone := regexp.MustCompile(`(?s)@media \(max-width: 600px\) \{.*?\.spend-grid \{ grid-template-columns: ([^;]+);`).FindStringSubmatch(string(css))
	if phone == nil {
		t.Fatal("the phone layout does not re-declare the grid, so it keeps five tracks with two empty")
	}
	if n := strings.Count(phone[1], "max-content") + strings.Count(phone[1], "minmax"); n != 3 {
		t.Errorf("phone grid-template-columns = %q, want three tracks", phone[1])
	}
	if !strings.Contains(string(css), ".spend-grid > .sg-row { display: contents; }") {
		t.Error("rows are not display:contents, so each one makes its own columns")
	}
}
