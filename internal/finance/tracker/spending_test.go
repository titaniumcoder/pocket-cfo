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
	tx := SpendingTx{ID: "b0442e17", Date: "03.08.2026", ISODate: "2026-08-03", Description: "LIDL SOFIA 4412", Cents: 21040}
	want := "Change ID b0442e17 (2026-08-03 / LIDL SOFIA 4412 / 210.40) like this: "
	if got := tx.ChangeRequest(); got != want {
		t.Errorf("ChangeRequest() = %q, want %q", got, want)
	}
	// Rounding to whole euros would match the wrong statement line.
	if strings.Contains(tx.ChangeRequest(), "/ 210)") {
		t.Error("the amount was rounded")
	}
	// The screen reads 03.08.2026 and this deliberately does not: pasted to
	// Hermes, a day-first date could be read as the 8th of January.
	if strings.Contains(tx.ChangeRequest(), "03.08.2026") {
		t.Error("the copy text uses the display format, which is ambiguous to a reader that does not know the convention")
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
	for _, want := range []string{`class="periodnav`, `href="/2026/7/spending"`, ">Today<", ">Reload<"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %s — it should carry the same chrome as every other page", want)
		}
	}
	// The Overview/Spending toggle is gone: the site menu carries both, and
	// its Finance entry already holds the month you are reading.
	if strings.Contains(body, ">Overview<") {
		t.Error("the internal toggle is back, duplicating the menu")
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

	// The behaviour moved to static/app.js when the CSP stopped inline scripts
	// running (see csp_test.go), so the fallback is asserted where it now lives
	// rather than in the page that used to carry it.
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "static", "app.js"))
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	js := string(b)

	if !strings.Contains(js, "if (navigator.clipboard)") {
		t.Error("the copy link assumes a secure context")
	}
	for _, want := range []string{"execCommand", "flash(ok ? 'copied' : 'failed')"} {
		if !strings.Contains(js, want) {
			t.Errorf("no %s fallback; the link would fail silently over plain HTTP", want)
		}
	}
	if !strings.Contains(js, "classList.add(state)") {
		t.Error("the copy control never flashes its confirmation")
	}

	// The control is an icon, and both states ship inside the link so the
	// confirmation swaps rather than rewrites — rewriting the contents is
	// what would leave an empty control behind.
	if strings.Contains(body, `title="Copy a change request for Hermes">copy</a>`) {
		t.Error("the copy control is still a word")
	}
	for _, want := range []string{`class="i-copy"`, `class="i-done"`} {
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
		if !strings.Contains(row[1], `class="stack-m"`) {
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
			  {"id":"t3","date":"2026-08-05","description":"X","amount":9,"account":"A","ignored":"own account"},
			  {"id":"t4","date":"2026-08-06","description":"To Rico Metzger","amount":5000,"account":"Company Checking","ignored":"owner draw","movement":"owner_draw"}]}`,
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

// countTracks splits a grid-template-columns value into tracks, treating a
// parenthesised function as one — strings.Fields sees minmax(0, 1fr) as two.
func countTracks(value string) int {
	depth, tracks, inTrack := 0, 0, false
	for _, r := range value {
		switch {
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ' ' && depth == 0:
			inTrack = false
			continue
		}
		if !inTrack {
			tracks++
			inTrack = true
		}
	}
	return tracks
}

// tableLikeGrids are the grids that behave like tables: rows of figures that
// have to line up down a page. Every one of them declares its columns.
var tableLikeGrids = []string{".ledger", ".spend-grid", ".cov-grid"}

type gridRule struct {
	tracks string
	offset int
	base   bool // the declaration that also turns the grid on
}

func gridRulesFor(css, selector string) []gridRule {
	// Anchored on a line break or a brace so ".ledger" does not also match
	// ".income-panel .ledger", which is a different, more specific rule.
	re := regexp.MustCompile(`[\n{]\s*` + regexp.QuoteMeta(selector) + ` \{([^}]*)\}`)
	var out []gridRule
	for _, m := range re.FindAllStringSubmatchIndex(css, -1) {
		body := css[m[2]:m[3]]
		tracks := regexp.MustCompile(`grid-template-columns: ([^;]+);`).FindStringSubmatch(body)
		if tracks == nil {
			continue
		}
		out = append(out, gridRule{tracks: tracks[1], offset: m[0], base: strings.Contains(body, "display: grid")})
	}
	return out
}

func appCSS(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestTableLikeGridsDeclareTheirColumns is the rule the whole app follows now:
// a grid whose rows are figures declares its widths.
//
// auto and max-content re-measure whenever the content changes, and on these
// pages the content changes on a click — opening a group adds rows with longer
// labels than the collapsed header had, every track resizes, and each figure
// slides sideways. Consistent columns and content sizing are not compatible;
// the columns win.
func TestTableLikeGridsDeclareTheirColumns(t *testing.T) {
	css := appCSS(t)
	for _, selector := range tableLikeGrids {
		t.Run(selector, func(t *testing.T) {
			rules := gridRulesFor(css, selector)
			if len(rules) < 2 {
				t.Fatalf("found %d declarations for %s, want a base and at least one narrower one", len(rules), selector)
			}
			var base *gridRule
			for i := range rules {
				for _, sized := range []string{"auto", "max-content", "min-content", "fit-content"} {
					if regexp.MustCompile(`\b` + sized + `\b`).MatchString(rules[i].tracks) {
						t.Errorf("%s is sized by %s: %q — the columns move when the content does", selector, sized, rules[i].tracks)
					}
				}
				if rules[i].base {
					base = &rules[i]
				}
			}
			if base == nil {
				t.Fatalf("%s never turns its grid on", selector)
			}
			// Media queries add no specificity, so a narrower rule written
			// before the base simply loses. That shipped once: the phone rules
			// for the spending page did nothing at all, and the description
			// column rendered one character per line.
			for _, r := range rules {
				if !r.base && r.offset < base.offset {
					t.Errorf("%s has an override at byte %d, before its base at %d — later wins at equal specificity, so it does nothing", selector, r.offset, base.offset)
				}
			}
		})
	}
}

// TestSpendingGridTrackCounts pins the two shapes by name: five columns on a
// desktop, four on a phone once the account is dropped.
func TestSpendingGridTrackCounts(t *testing.T) {
	css := appCSS(t)
	for _, r := range gridRulesFor(css, ".spend-grid") {
		want := 4
		if r.base {
			want = 5
		}
		if got := countTracks(r.tracks); got != want {
			t.Errorf("grid-template-columns = %q has %d tracks, want %d", r.tracks, got, want)
		}
	}
	if !strings.Contains(css, ".spend-grid > .sg-row { display: contents; }") {
		t.Error("rows are not display:contents, so each one makes its own columns")
	}
}

// TestDatesReadDayFirst: the files store ISO because that is what sorts and
// validates, but nothing on screen should — the invoices have always been
// day-first and the spending page was the odd one out.
func TestDatesReadDayFirst(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-09","imported_at":"2026-08-10"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000001"}]}`,
	})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)
	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	body := rec.Body.String()

	for _, want := range []string{">03.08.2026<", ">01.08.2026<", ">09.08.2026<", ">10.08.2026<"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s — a transaction or coverage date is still ISO", want)
		}
	}
	// The copy text is the one place ISO survives, so finding it in an
	// attribute is expected; finding it in the page's text is not.
	visible := regexp.MustCompile(`data-copy="[^"]*"`).ReplaceAllString(body, "")
	if strings.Contains(visible, "2026-08-03") {
		t.Error("an ISO date is still rendered on the page")
	}
}

// TestAccountAsOfReadsDayFirst: the same date, in the same house format, in
// the one place a read date is now shown.
func TestAccountAsOfReadsDayFirst(t *testing.T) {
	v := actualsTracker(t, nil).ComputeSpending(context.Background(), 2026, time.August)
	if len(v.Balances) == 0 {
		t.Fatal("the fixture has no balances")
	}
	for _, a := range v.Balances {
		if strings.Contains(a.AsOf, "-") {
			t.Errorf("account %q is as of %q, still ISO", a.Name, a.AsOf)
		}
	}
}

// TestSpendingShowsTheBalanceAndWhenItWasRead: the dashboard is a page of
// figures and says only what each account holds. The read date is the context
// for that figure, and this is where it goes — the page you are on when you
// are reconciling against a statement, and so the page where knowing the bank
// was last read six weeks ago actually changes what you do next.
func TestSpendingShowsTheBalanceAndWhenItWasRead(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"Revolut Private","kind":"private","balances":[
			{"as_of":"2026-04-30","balance":3950},
			{"as_of":"2026-07-31","balance":4200}
		]},
		{"name":"Revolut Business","kind":"company","balances":[{"as_of":"2026-07-31","balance":-10}]}
	]}`)
	v := trk.ComputeSpending(context.Background(), 2026, time.August)

	if len(v.Balances) != 2 {
		t.Fatalf("balances = %+v, want both accounts", v.Balances)
	}
	byName := map[string]AccountRow{}
	for _, b := range v.Balances {
		byName[b.Name] = b
	}
	if got := byName["Revolut Private"]; got.Cents != 420000 || got.AsOf != "31.07.2026" {
		t.Errorf("Revolut Private = %+v, want the 31.07.2026 reading of 4 200", got)
	}
	if got := byName["Revolut Business"]; got.Cents != -1000 {
		t.Errorf("Revolut Business = %+v, want the overdrawn figure carried as-is", got)
	}

	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	body := rec.Body.String()
	for _, want := range []string{"Balances", "Revolut Private", "31.07.2026", "4,200"} {
		if !strings.Contains(body, want) {
			t.Errorf("the spending page never says %q", want)
		}
	}
}

// TestSpendingBalancesFollowTheViewedMonth: the figure shown is the one that
// was true for the month on screen, not the newest one in the file. Paging
// back to May must not report a balance read at the end of July.
func TestSpendingBalancesFollowTheViewedMonth(t *testing.T) {
	trk := accountsTracker(t, `{"accounts":[
		{"name":"P","kind":"private","balances":[
			{"as_of":"2026-04-30","balance":3950},
			{"as_of":"2026-07-31","balance":4200}
		]}
	]}`)
	may := trk.ComputeSpending(context.Background(), 2026, time.May)
	if len(may.Balances) != 1 || may.Balances[0].AsOf != "30.04.2026" || may.Balances[0].Cents != 395000 {
		t.Errorf("May balances = %+v, want the 30.04.2026 reading", may.Balances)
	}
	april := trk.ComputeSpending(context.Background(), 2026, time.April)
	if len(april.Balances) != 0 {
		t.Errorf("April balances = %+v, want none — the 30 April read closes April", april.Balances)
	}
}

// TestRowRulesEndTogether: each cell draws its own bottom rule, so they only
// line up if every cell is as tall as the row. A baseline-aligned cell is only
// as tall as its own content — a description wrapping to three lines drew
// three rules at three different heights.
func TestRowRulesEndTogether(t *testing.T) {
	css := appCSS(t)
	for _, selector := range []string{".spend-grid", ".cov-grid"} {
		rule := regexp.MustCompile(regexp.QuoteMeta(selector) + ` \{ display: grid;[^}]*\}`).FindString(css)
		if rule == "" {
			t.Fatalf("no base rule for %s", selector)
		}
		if strings.Contains(rule, "align-items: baseline") {
			t.Errorf("%s aligns to the baseline, so a wrapped row draws its rules at different heights", selector)
		}
		if !strings.Contains(rule, "align-items: stretch") {
			t.Errorf("%s does not stretch its cells to the row height: %s", selector, rule)
		}
	}
}

// untrackedActualsJSON: a whole line nobody has placed, a split with one part
// still undecided, and an ordinary categorised line to compare against.
const untrackedActualsJSON = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
	"transactions":[
		{"id":"x1","date":"2026-08-03","description":"LIDL","amount":1000,"account":"A","category":"rent"},
		{"id":"x2","date":"2026-08-14","description":"ATM WITHDRAWAL","amount":100,"account":"A","untracked":"cash, not spent yet"},
		{"id":"x3","date":"2026-08-20","description":"ATM VARNA","amount":50,"account":"A","splits":[
			{"amount":20,"category":"rent"},
			{"amount":30,"untracked":"still in my wallet"}]}]}`

// TestSpendingPageShowsUntrackedCash: the money is excluded from every figure,
// so the page listing it is the only thing that stops it being forgotten.
func TestSpendingPageShowsUntrackedCash(t *testing.T) {
	trk := actualsTracker(t, map[string]string{"actuals/2026-08.json": untrackedActualsJSON})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)

	if v.UntrackedCount != 2 {
		t.Errorf("UntrackedCount = %d, want 2 — the whole line and the split's part", v.UntrackedCount)
	}
	if v.UntrackedCents != 13000 {
		t.Errorf("UntrackedCents = %d, want 13000 — 100 plus the split's 30", v.UntrackedCents)
	}
	// The page's own total is categorised money only.
	if v.TotalCents != 102000 {
		t.Errorf("TotalCents = %d, want 102000 — the 1000 line and the split's categorised 20, and neither untracked amount", v.TotalCents)
	}
	if len(v.Ignored) != 0 {
		t.Errorf("untracked money landed in the ignored bucket: %+v", v.Ignored)
	}

	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	body := rec.Body.String()
	for _, want := range []string{"Untracked cash", "cash, not spent yet", "still in my wallet", "untracked cash"} {
		if !strings.Contains(body, want) {
			t.Errorf("the spending page never says %q", want)
		}
	}
	// The month is marked, so an untracked line is visible before scrolling.
	if !strings.Contains(body, "untracked</span></h2>") {
		t.Error("the month title carries no untracked marker")
	}
	// And the picker marks it, so the other months can see it too.
	if !strings.Contains(body, "August &bull;</option>") {
		t.Errorf("the month picker does not mark August")
	}
}

// TestUntrackedCashNamesTheAccountItLeft: knowing 42 went missing is not
// actionable until you know which card it went missing from, and with the
// account in the third track — where every other section on the page keeps it
// — the note moves in beside the description rather than being dropped. The
// row still contributes five cells, which TestSpendingIsOneGrid pins for the
// whole page.
func TestUntrackedCashNamesTheAccountItLeft(t *testing.T) {
	trk := actualsTracker(t, map[string]string{"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"Revolut Private","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[
			{"id":"u1","date":"2026-08-13","description":"10aug Pvvpanazl","amount":42,"account":"Revolut Private","untracked":"merchant not identified from the statement"}]}`})

	rec := httptest.NewRecorder()
	RenderSpending(rec, trk.ComputeSpending(context.Background(), 2026, time.August))
	body := rec.Body.String()

	row := regexp.MustCompile(`(?s)<div class="sg-row sg-untracked-row">(.*?)</div>`).FindStringSubmatch(body)
	if row == nil {
		t.Fatal("no untracked row on the page")
	}
	if !strings.Contains(row[1], `<span class="col-secondary">Revolut Private</span>`) {
		t.Errorf("the untracked row does not name the account it left: %s", row[1])
	}
	if !strings.Contains(row[1], `<span class="untracked-note">merchant not identified from the statement</span>`) {
		t.Errorf("the note did not move in beside the description: %s", row[1])
	}
	if got := topLevelCells(row[1]); got != 5 {
		t.Errorf("the untracked row contributes %d cells, want 5 — the note has to nest inside the description, not take a track", got)
	}
	// The header names what the column now holds, or the figures line up under
	// the wrong word.
	if !strings.Contains(body, `<span class="sg-head col-secondary">Account</span><span class="sg-head num">Amount</span>`) {
		t.Error("the untracked header still says Note over the account column")
	}
}

// TestUntrackedCashLeadsThePage: it is the one section on the page that asks
// to be acted on, so it is read before the categories rather than found after
// scrolling past them.
func TestUntrackedCashLeadsThePage(t *testing.T) {
	trk := actualsTracker(t, map[string]string{"actuals/2026-08.json": untrackedActualsJSON})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)

	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	body := rec.Body.String()

	untracked := strings.Index(body, "Untracked cash")
	if untracked < 0 {
		t.Fatal("the page never says Untracked cash")
	}
	grid := strings.Index(body, `<div class="spend-grid">`)
	if grid < 0 || untracked < grid {
		t.Error("untracked cash escaped the one transaction grid, so its columns no longer line up with the rest of the page")
	}
	for _, after := range []string{`<span class="sg-head col-secondary">Account</span>`, "Not in this month", "Not budget expenses"} {
		at := strings.Index(body, after)
		if at < 0 {
			t.Errorf("the page never says %q", after)
			continue
		}
		if untracked > at {
			t.Errorf("untracked cash comes after %q", after)
		}
	}
}

// TestUntrackedCashIsSetInWeight guards the half of the change the template
// test cannot see: the rows themselves, not just their heading, are bold.
func TestUntrackedCashIsSetInWeight(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		".spend-grid > h3.sg-untracked { font-weight: 700; }",
		".spend-grid > .sg-untracked-row > * { font-weight: 700; }",
		".spend-grid > h3.sg-untracked:first-child { margin-top: 0; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the stylesheet no longer carries %q", want)
		}
	}
}

// TestUntrackedCashReachesNoFinanceFigure is the promise the whole feature
// rests on: it is visible on the spending page and nowhere in the money.
func TestUntrackedCashReachesNoFinanceFigure(t *testing.T) {
	ctx := context.Background()

	// The same file twice, once with the two untracked amounts and once
	// without, so any figure that moved was reading money it should not.
	withOnly := `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[
			{"id":"x1","date":"2026-08-03","description":"LIDL","amount":1000,"account":"A","category":"rent"},
			{"id":"x3","date":"2026-08-20","description":"ATM VARNA","amount":20,"account":"A","category":"rent"}]}`

	with := actualsTracker(t, map[string]string{"actuals/2026-08.json": untrackedActualsJSON}).ComputeMonth(ctx, 2026, time.August)
	without := actualsTracker(t, map[string]string{"actuals/2026-08.json": withOnly}).ComputeMonth(ctx, 2026, time.August)

	if with.UntrackedCents != 13000 || with.UntrackedCount != 2 {
		t.Fatalf("the untracked figures did not reach Figures (%d/%d) — the rest of this test would prove nothing",
			with.UntrackedCents, with.UntrackedCount)
	}
	for _, f := range []struct {
		name          string
		with, without int
	}{
		{"PrivateActualCents", with.PrivateActualCents, without.PrivateActualCents},
		{"CompanyActualCents", with.CompanyActualCents, without.CompanyActualCents},
		{"PrivateUnmatchedCents", with.PrivateUnmatchedCents, without.PrivateUnmatchedCents},
		{"CompanyUnmatchedCents", with.CompanyUnmatchedCents, without.CompanyUnmatchedCents},
		{"BalanceCents", with.BalanceCents, without.BalanceCents},
		{"AvailableCents", with.AvailableCents, without.AvailableCents},
		{"PrivateTotalPlannedCents", with.PrivateTotalPlannedCents, without.PrivateTotalPlannedCents},
	} {
		if f.with != f.without {
			t.Errorf("%s = %d with untracked cash, %d without — it must reach no figure", f.name, f.with, f.without)
		}
	}

	// The dashboard marks the month, but never prints the amount: the place to
	// deal with it is the spending page.
	with.SpendingDetailURL = "/2026/8/spending"
	rec := httptest.NewRecorder()
	RenderPage(rec, with)
	body := rec.Body.String()
	if !strings.Contains(body, "untracked") {
		t.Error("the dashboard does not mark a month with untracked cash")
	}
	if strings.Contains(body, "130") {
		t.Error("the dashboard printed the untracked amount; it belongs to no figure on that page")
	}
}

// TestALedgerWithoutActualsPutsItsFigureInTheAmountColumn: the year view has no
// actuals, and its category rows used to leave the planned figure in .mid — the
// secondary column, at .6 opacity — while the group total it rolls into sat in
// .amt. The figures did not line up with their own subtotal and every one of
// them read as a footnote. On a phone, where .mid is the only column left, it
// was the whole page.
func TestALedgerWithoutActualsPutsItsFigureInTheAmountColumn(t *testing.T) {
	f := Figures{
		Currency: "€",
		PrivateGroups: []CategoryGroupView{{
			Name:         "Housing",
			PlannedCents: 90000,
			Rows:         []CategoryRow{{Name: "Rent", CategoryID: "rent", PlannedCents: 90000}},
		}},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()

	row := regexp.MustCompile(`(?s)<div class="row[^"]*">\s*<span class="label">Rent.*?</div>`).FindString(body)
	if row == "" {
		t.Fatal("no Rent row rendered")
	}
	mid := regexp.MustCompile(`(?s)<span class="mid[^"]*">(.*?)</span>`).FindStringSubmatch(row)
	amt := regexp.MustCompile(`(?s)<span class="amt[^"]*">(.*?)</span>`).FindStringSubmatch(row)
	if mid == nil || amt == nil {
		t.Fatalf("row has no mid/amt pair:\n%s", row)
	}
	if strings.TrimSpace(mid[1]) != "" {
		t.Errorf("the middle column carries %q with no actuals to compare against", mid[1])
	}
	if !strings.Contains(amt[1], "900") {
		t.Errorf("the amount column is %q, want the planned figure", amt[1])
	}
	// Which is where the group total already was, so the two now line up.
	header := regexp.MustCompile(`(?s)<div class="group-header".*?</div>`).FindString(body)
	if !strings.Contains(header, `<span class="mid"></span>`) {
		t.Errorf("the group header stopped agreeing with its rows:\n%s", header)
	}
}

// movementActualsJSON: one line marked as money crossing to the owner, one
// tax payment that leaves the company without reaching him, one ordinary
// ignored line and one categorised line to compare against.
const movementActualsJSON = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
	"transactions":[
		{"id":"m0","date":"2026-08-03","description":"LIDL","amount":1000,"account":"A","category":"rent"},
		{"id":"m1","date":"2026-08-06","description":"To Rico Metzger","amount":5000,"account":"Company Checking","ignored":"owner draw — the receiving side is on the private statement","movement":"owner_draw"},
		{"id":"m2","date":"2026-08-07","description":"NRA","amount":1000,"account":"Company Checking","ignored":"corporate tax","movement":"corporate_tax"},
		{"id":"m3","date":"2026-08-08","description":"SAVINGS","amount":300,"account":"A","ignored":"transfer to my own savings account"}]}`

// TestAMarkedTransferIsListedOnceAndNotAlsoUnderNotBudgetExpenses: a marked
// line carries an ignored reason too, so the page would otherwise show it in
// both places and count it twice.
func TestAMarkedTransferIsListedOnceAndNotAlsoUnderNotBudgetExpenses(t *testing.T) {
	trk := actualsTracker(t, map[string]string{"actuals/2026-08.json": movementActualsJSON})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)

	if len(v.Movements) != 2 {
		t.Fatalf("got %d movement sections, want the draw and the tax: %+v", len(v.Movements), v.Movements)
	}
	if len(v.Ignored) != 1 || v.IgnoredCount != 1 {
		t.Errorf("Not budget expenses holds %d line(s) and counts %d, want only the savings transfer", len(v.Ignored), v.IgnoredCount)
	}
	for _, ig := range v.Ignored {
		if strings.Contains(ig.Description, "Rico") || ig.Description == "NRA" {
			t.Errorf("a marked line is listed under Not budget expenses as well: %+v", ig)
		}
	}

	rec := httptest.NewRecorder()
	RenderSpending(rec, v)
	body := rec.Body.String()
	// Counted as rows rather than as text: the description also rides inside
	// the copy link's payload, which is not a second listing of it.
	if n := strings.Count(body, `<span class="sg-desc">To Rico Metzger`); n != 1 {
		t.Errorf("the draw is listed %d times on the page, want once", n)
	}
	for _, want := range []string{"Owner draw", "Company profit tax paid"} {
		if !strings.Contains(body, want) {
			t.Errorf("the spending page never says %q", want)
		}
	}
}

// TestTheTaxPaymentsAreVisibleAndFeedNoFigure: they earn a section only so the
// page still reconciles line-for-line against the statement.
func TestOwnerMovementsReachNoFinanceFigure(t *testing.T) {
	const withMarkers = movementActualsJSON
	const unmarked = `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[
			{"id":"m0","date":"2026-08-03","description":"LIDL","amount":1000,"account":"A","category":"rent"},
			{"id":"m1","date":"2026-08-06","description":"To Rico Metzger","amount":5000,"account":"Company Checking","ignored":"owner draw — the receiving side is on the private statement"},
			{"id":"m2","date":"2026-08-07","description":"NRA","amount":1000,"account":"Company Checking","ignored":"corporate tax"},
			{"id":"m3","date":"2026-08-08","description":"SAVINGS","amount":300,"account":"A","ignored":"transfer to my own savings account"}]}`

	marked := actualsTracker(t, map[string]string{"actuals/2026-08.json": withMarkers}).
		ComputeSpending(context.Background(), 2026, time.August)
	plain := actualsTracker(t, map[string]string{"actuals/2026-08.json": unmarked}).
		ComputeSpending(context.Background(), 2026, time.August)

	if marked.TotalCents != plain.TotalCents {
		t.Errorf("the page total moved from %d to %d because lines were marked", plain.TotalCents, marked.TotalCents)
	}
	if marked.UntrackedCents != plain.UntrackedCents {
		t.Errorf("untracked cash moved from %d to %d", plain.UntrackedCents, marked.UntrackedCents)
	}
	if len(marked.Movements) == 0 {
		t.Fatal("nothing was marked, so this test proves nothing")
	}
}

// TestMovementSectionsKeepTheirOrderWhateverTheStatementOrder: the sections
// follow the schema's own order, so the page does not reshuffle between two
// reads of the same file.
func TestMovementSectionsKeepTheirOrderWhateverTheStatementOrder(t *testing.T) {
	trk := actualsTracker(t, map[string]string{"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
		"transactions":[
			{"id":"z1","date":"2026-08-09","description":"NRA","amount":1000,"account":"C","ignored":"corporate tax","movement":"corporate_tax"},
			{"id":"z2","date":"2026-08-08","description":"From Rico","amount":-500,"account":"C","ignored":"paid in","movement":"owner_contribution"},
			{"id":"z3","date":"2026-08-07","description":"To Rico","amount":5000,"account":"C","ignored":"draw","movement":"owner_draw"}]}`})
	v := trk.ComputeSpending(context.Background(), 2026, time.August)

	var got []string
	for _, g := range v.Movements {
		got = append(got, g.Name)
	}
	want := []string{"Owner draw", "Paid into the company", "Company profit tax paid"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("sections = %v, want %v", got, want)
	}
}
