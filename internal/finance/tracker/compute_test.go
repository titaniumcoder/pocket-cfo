package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFillMonthNav(t *testing.T) {
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	var f Figures
	f.fillMonthNav(now, start)

	if f.Mode != "month" || f.Year != 2026 || f.MonthNum != 3 {
		t.Errorf("mode/year/month = %q/%d/%d", f.Mode, f.Year, f.MonthNum)
	}
	if f.PrevURL != "/2026/2" || f.NextURL != "/2026/4" {
		t.Errorf("prev/next = %q/%q", f.PrevURL, f.NextURL)
	}
	if f.MonthViewURL != "/2026/3" || f.YearViewURL != "/2026" {
		t.Errorf("view urls = %q/%q", f.MonthViewURL, f.YearViewURL)
	}
	if f.RefreshURL != "/2026/3?refresh=1" {
		t.Errorf("refresh url = %q", f.RefreshURL)
	}
	if len(f.Months) != 12 || f.Months[0].Name != "January" {
		t.Errorf("months not filled: %+v", f.Months)
	}
	if len(f.Years) != 5 {
		t.Errorf("years = %v", f.Years)
	}
}

func TestFillYearNav(t *testing.T) {
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	// Current year: switching to month view lands on the current month.
	var cur Figures
	cur.fillYearNav(now, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if cur.Mode != "year" || cur.PrevURL != "/2025" || cur.NextURL != "/2027" {
		t.Errorf("year nav = %q %q %q", cur.Mode, cur.PrevURL, cur.NextURL)
	}
	if cur.MonthViewURL != "/2026/6" {
		t.Errorf("current-year month-view url = %q, want /2026/6", cur.MonthViewURL)
	}
	var min Figures
	min.fillYearNav(now, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	if !min.PrevDisabled || min.PrevURL != "" || min.NextDisabled {
		t.Errorf("min-year arrows = prevDisabled:%v prev:%q nextDisabled:%v", min.PrevDisabled, min.PrevURL, min.NextDisabled)
	}
	var max Figures
	max.fillYearNav(now, time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC))
	if !max.NextDisabled || max.NextURL != "" || max.PrevDisabled {
		t.Errorf("max-year arrows = nextDisabled:%v next:%q prevDisabled:%v", max.NextDisabled, max.NextURL, max.PrevDisabled)
	}

	// Other year: month view lands on January.
	var other Figures
	other.fillYearNav(now, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if other.MonthViewURL != "/2025/1" {
		t.Errorf("other-year month-view url = %q, want /2025/1", other.MonthViewURL)
	}
}

// fullTracker builds a Tracker whose Toggl + Holidays sources are backed by a
// fake returning one billable project, one named project, and one holiday.
func fullTracker() *Tracker {
	row := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-03-02T09:00:00+00:00"}]}]`
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return row, "", "" },
		projects: `[{"id":1,"name":"Alpha"}]`,
		holidays: `[{"startDate":"2026-03-19","endDate":"2026-03-19","name":[{"language":"DE","text":"Josephstag"}]}]`,
	}
	client := b.transport()
	return &Tracker{
		Toggl:        &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:     &Holidays{HTTP: client},
		HoursPerDay:  8,
		Loc:          time.UTC,
		VacationDays: 25,
		RateCents:    7500,
		RateCurrency: "EUR",
		Personal: PersonalParams{
			EmployerRate: 0.1892, EmployeeRate: 0.1378,
			MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10,
		},
	}
}

func TestComputeMonthHappyPath(t *testing.T) {
	trk := fullTracker()
	// A past month so "today" never falls inside, keeping the result deterministic.
	f := trk.ComputeMonth(context.Background(), 2026, time.March)

	if f.TrackedErr != "" {
		t.Fatalf("TrackedErr = %q", f.TrackedErr)
	}
	if len(f.Tracked) != 1 || f.Tracked[0].Project != "Alpha" {
		t.Errorf("tracked rows = %+v", f.Tracked)
	}
	if f.Tracked[0].AmountCents != 150000 {
		t.Errorf("amount = %d, want 150000", f.Tracked[0].AmountCents)
	}
	if f.Currency != "€" {
		t.Errorf("currency = %q, want €", f.Currency)
	}
	if f.TotalErr != "" {
		t.Errorf("TotalErr = %q", f.TotalErr)
	}
	if f.Personal.Err != "" {
		t.Errorf("Personal.Err = %q", f.Personal.Err)
	}
	if len(f.Holidays) != 1 || !strings.Contains(f.Holidays[0].Name, "Josephstag") {
		t.Errorf("holidays = %+v", f.Holidays)
	}
	if f.Mode != "month" {
		t.Errorf("Mode = %q, want month", f.Mode)
	}
}

func TestComputeYearShowsFutureVacation(t *testing.T) {
	trk := fullTracker()
	f := trk.ComputeYear(context.Background(), 2027)
	if f.Mode != "year" {
		t.Errorf("Mode = %q, want year", f.Mode)
	}
	if !f.ShowVacation || f.VacationRemaining != 25 || f.VacationHoursDeducted != "200" {
		t.Errorf("vacation not shown: show=%v remaining=%d hours=%q", f.ShowVacation, f.VacationRemaining, f.VacationHoursDeducted)
	}
	if f.ExpectedNetCents >= f.ExpectedCents {
		t.Errorf("expected total should deduct vacation: gross=%d net=%d", f.ExpectedCents, f.ExpectedNetCents)
	}
}

func TestComputeYearHidesVacationWhenNoneRemaining(t *testing.T) {
	trk := fullTracker()
	f := trk.ComputeYear(context.Background(), 2026)
	if f.ShowVacation {
		t.Errorf("ShowVacation = true, want false when no vacation remains")
	}
}

func TestComputeYearPersonalIncomeUsesMonthlyBreakdowns(t *testing.T) {
	row := `[
		{"project_id":1,"hourly_rate_in_cents":10000,"billable_amount_in_cents":100000,"currency":"EUR","time_entries":[{"seconds":36000,"start":"2025-06-02T09:00:00+00:00"}]},
		{"project_id":1,"hourly_rate_in_cents":10000,"billable_amount_in_cents":300000,"currency":"EUR","time_entries":[{"seconds":108000,"start":"2025-07-02T09:00:00+00:00"}]},
		{"project_id":1,"hourly_rate_in_cents":10000,"billable_amount_in_cents":1000000,"currency":"EUR","time_entries":[{"seconds":360000,"start":"2025-08-02T09:00:00+00:00"}]}
	]`
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return row, "", "" },
		projects: `[{"id":1,"name":"Alpha"}]`,
	}
	client := b.transport()
	p := params()
	trk := &Tracker{
		Toggl:        &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:     &Holidays{HTTP: client},
		HoursPerDay:  8,
		Loc:          time.UTC,
		VacationDays: 0,
		Personal:     p,
	}

	f := trk.ComputeYear(context.Background(), 2025)
	want := p.breakdownMonths([]float64{0, 0, 0, 0, 0, 1000, 3000, 10000, 0, 0, 0, 0}, nil)
	smoothed := p.breakdown(14000, 0, 12)

	if f.Personal.Err != "" {
		t.Fatalf("Personal.Err = %q", f.Personal.Err)
	}
	if f.TotalCents != 1400000 {
		t.Fatalf("TotalCents = %d, want 1400000", f.TotalCents)
	}
	if f.Personal.CompanyIncomeCents != want.CompanyIncomeCents {
		t.Errorf("company = %d, want %d", f.Personal.CompanyIncomeCents, want.CompanyIncomeCents)
	}
	if f.Personal.GrossSalaryCents != want.GrossSalaryCents {
		t.Errorf("gross = %d, want %d", f.Personal.GrossSalaryCents, want.GrossSalaryCents)
	}
	if f.Personal.NetIncomeCents != want.NetIncomeCents {
		t.Errorf("net = %d, want %d", f.Personal.NetIncomeCents, want.NetIncomeCents)
	}
	if f.Personal.NetIncomeCents == smoothed.NetIncomeCents {
		t.Errorf("year personal income still looks like annual smoothing")
	}
}

func TestComputeTrackedErrorDegrades(t *testing.T) {
	// Detailed fails, but the page must still render (errors inline, no crash).
	b := &fakeBackend{failDetailed: 500, projects: `[]`}
	client := b.transport()
	trk := &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
	}
	f := trk.ComputeMonth(context.Background(), 2026, time.March)
	if f.TrackedErr == "" {
		t.Error("expected TrackedErr to be set")
	}
	if f.TotalErr == "" {
		t.Error("expected TotalErr to degrade to unavailable")
	}
	if f.Personal.Err == "" {
		t.Error("expected Personal.Err when total unavailable")
	}
}

func TestRenderPage(t *testing.T) {
	trk := fullTracker()
	f := trk.ComputeMonth(context.Background(), 2026, time.March)
	rec := httptest.NewRecorder()
	RenderPage(rec, f)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}
	body := rec.Body.String()
	// The full page — chrome, nav, and the ledger data — renders in one shot,
	// no separate loading step.
	for _, want := range []string{"PocketCFO", "Alpha", "Tracked", "Personal income (Bulgaria)", "Josephstag"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, ">Total<") {
		t.Error("standalone Total section should not render")
	}
	if !strings.Contains(body, "<html") {
		t.Error("page should be a full HTML document")
	}
}

func TestRenderPageDisablesBoundaryYearArrows(t *testing.T) {
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	var f Figures
	f.Month = "2028"
	f.Currency = "€"
	f.fillYearNav(now, time.Date(2028, time.January, 1, 0, 0, 0, 0, time.UTC))

	rec := httptest.NewRecorder()
	RenderPage(rec, f)
	body := rec.Body.String()
	if !strings.Contains(body, `class="arrow disabled" aria-disabled="true" aria-label="Next"`) {
		t.Error("next arrow should render disabled at the max year")
	}
	if strings.Contains(body, `href="/2029"`) {
		t.Error("next arrow should not link beyond the max year")
	}
}

func TestRenderPageHidesEmptyTrackedAndTotalWhenNoVacation(t *testing.T) {
	f := Figures{
		LastUpdated:   "—",
		Mode:          "year",
		Currency:      "€",
		ExpectedRange: "01.01. - 31.12.27",
		ExpectedHours: "1600",
		ExpectedRate:  "75",
		ExpectedCents: 12000000,
		TotalHours:    "1600:00",
		TotalRate:     "75",
		TotalCents:    12000000,
		Personal:      PersonalView{},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)

	body := rec.Body.String()
	if strings.Contains(body, ">Tracked<") {
		t.Error("empty tracked section should not render")
	}
	if strings.Contains(body, "Expected total") {
		t.Error("expected total row should only render with vacation")
	}
	if strings.Contains(body, ">Total<") {
		t.Error("standalone Total section should not render")
	}
	if !strings.Contains(body, "Personal income (Bulgaria)") {
		t.Error("personal income section should still render")
	}
}

// TestRenderPageInvoicedRowShowsNumber guards against a template/struct
// mismatch: InvoicedRow used to carry Project, now carries Number — a
// html/template range over a nonexistent field fails only at execution
// time, not at go build, so this needs its own explicit coverage.
func TestRenderPageInvoicedRowShowsNumber(t *testing.T) {
	f := Figures{
		LastUpdated: "—",
		Mode:        "month",
		Currency:    "€",
		Invoiced:    []InvoicedRow{{Number: "0001", AmountCents: 500000}},
		Personal:    PersonalView{},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)

	body := rec.Body.String()
	if !strings.Contains(body, ">0001<") {
		t.Errorf("expected the invoice number 0001 to render as plain text (no URL set), got: %s", body)
	}
	if strings.Contains(body, "500000") {
		// sanity: eur formats cents, so the raw int shouldn't appear literally
		t.Error("AmountCents should render formatted, not as a raw integer")
	}
}

// TestRenderPageInvoicedRowLinksWhenURLSet confirms an InvoicedRow with a
// URL (a session with invoicing rights, see cmd/pocketcfo's
// fillInvoiceLinks) renders as a link to that URL, not just plain text.
func TestRenderPageInvoicedRowLinksWhenURLSet(t *testing.T) {
	f := Figures{
		LastUpdated: "—",
		Mode:        "month",
		Currency:    "€",
		Invoiced:    []InvoicedRow{{Number: "0001", AmountCents: 500000, URL: "/invoicing/invoices/0001.pdf"}},
		Personal:    PersonalView{},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)

	body := rec.Body.String()
	if !strings.Contains(body, `<a href="/invoicing/invoices/0001.pdf">0001</a>`) {
		t.Errorf("expected a link to the invoice PDF, got: %s", body)
	}
}

func TestRenderPageShowsExpectedTotalWithVacation(t *testing.T) {
	f := Figures{
		LastUpdated:           "—",
		Mode:                  "year",
		Currency:              "€",
		ExpectedRange:         "01.01. - 31.12.27",
		ExpectedHours:         "1800",
		ExpectedRate:          "75",
		ExpectedCents:         13500000,
		ShowVacation:          true,
		VacationHoursDeducted: "200",
		VacationCentsDeducted: 1500000,
		ExpectedNetHours:      "1600",
		ExpectedNetCents:      12000000,
		TotalHours:            "1600:00",
		TotalRate:             "75",
		TotalCents:            12000000,
		Personal:              PersonalView{},
	}
	rec := httptest.NewRecorder()
	RenderPage(rec, f)

	body := rec.Body.String()
	if !strings.Contains(body, "Expected total") {
		t.Error("expected total row should render with vacation")
	}
	if strings.Contains(body, ">Total<") {
		t.Error("standalone Total section should not render")
	}
	if !strings.Contains(body, "Personal income (Bulgaria)") {
		t.Error("personal income section should still render")
	}
}

func TestRenderLogin(t *testing.T) {
	rec := httptest.NewRecorder()
	RenderLogin(rec, "", true)
	if !strings.Contains(rec.Body.String(), "Continue with GitHub") {
		t.Error("login page should contain Continue with GitHub")
	}
	if !strings.Contains(rec.Body.String(), `href="/auth/login"`) {
		t.Error("GitHub login link should point at the registered /auth/login route")
	}
	if !strings.Contains(rec.Body.String(), "Continue with email") {
		t.Error("login page should contain Continue with email when showEmailLogin is true")
	}
	if strings.Contains(rec.Body.String(), "class=\"error\"") {
		t.Error("login without message should not show error")
	}

	rec2 := httptest.NewRecorder()
	RenderLogin(rec2, "Wrong password.", true)
	if !strings.Contains(rec2.Body.String(), "Wrong password.") {
		t.Error("login with message should show it")
	}

	rec3 := httptest.NewRecorder()
	RenderLogin(rec3, "", false)
	if strings.Contains(rec3.Body.String(), "Continue with email") {
		t.Error("login page should not contain Continue with email when showEmailLogin is false")
	}
}

// TestInvoiceSuppressesTrackedAndAddsUsableIncome is an end-to-end
// regression test for PocketCFO's plan §2.3: "an invoice from 5.8 removes
// all previous hours/estimations till end of July; the money is usable in
// the month following the due date." Project 1 has real Toggl-tracked
// hours in July; an invoice issued 5 August (due 19 August) for that same
// project should make July's Tracked row disappear (superseded) while
// leaving August itself untouched (after the horizon), and should show up
// as real income only in September (due date's following month) — not in
// August, and not as an hours figure anywhere.
func TestInvoiceSuppressesTrackedAndAddsUsableIncome(t *testing.T) {
	rows := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-07-10T09:00:00+00:00"}]}]`
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return rows, "", "" },
		projects: `[{"id":1,"name":"Alpha","client_id":1}]`,
	}
	client := b.transport()
	trk := &Tracker{
		Toggl:        &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:     &Holidays{HTTP: client},
		HoursPerDay:  8,
		Loc:          time.UTC,
		RateCents:    7500,
		RateCurrency: "EUR",
		Personal: PersonalParams{
			EmployerRate: 0.1892, EmployeeRate: 0.1378,
			MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10,
		},
	}

	// Before any invoice exists, July shows the real tracked hours.
	beforeInvoice := trk.ComputeMonth(context.Background(), 2026, time.July)
	if len(beforeInvoice.Tracked) != 1 || beforeInvoice.Tracked[0].AmountCents != 150000 {
		t.Fatalf("before invoicing: Tracked = %+v, want one row of 150000", beforeInvoice.Tracked)
	}

	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{ClientID: 1, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})

	july := trk.ComputeMonth(context.Background(), 2026, time.July)
	if len(july.Tracked) != 0 {
		t.Errorf("July Tracked = %+v, want empty (superseded by the 5 Aug invoice)", july.Tracked)
	}
	if july.InvoicedCents != 0 {
		t.Errorf("July InvoicedCents = %d, want 0 (not yet usable)", july.InvoicedCents)
	}

	august := trk.ComputeMonth(context.Background(), 2026, time.August)
	if august.InvoicedCents != 0 {
		t.Errorf("August InvoicedCents = %d, want 0 (usable starts September)", august.InvoicedCents)
	}

	september := trk.ComputeMonth(context.Background(), 2026, time.September)
	if len(september.Invoiced) != 1 || september.Invoiced[0].AmountCents != 500000 {
		t.Fatalf("September Invoiced = %+v, want one row of 500000", september.Invoiced)
	}
	if september.Invoiced[0].Number != "0001" {
		t.Errorf("September Invoiced[0].Number = %q, want 0001", september.Invoiced[0].Number)
	}
	if september.InvoicedCents != 500000 {
		t.Errorf("September InvoicedCents = %d, want 500000", september.InvoicedCents)
	}
	if september.TotalCents < 500000 {
		t.Errorf("September TotalCents = %d, want at least the 500000 invoiced", september.TotalCents)
	}
}

// TestInvoiceSuppressesMultipleProjectsUnderSameClient covers the reason for
// resolving suppression via Toggl client rather than Toggl project: a client
// with several Toggl projects must have all of them suppressed together once
// invoiced, not just the one project a bare project-ID key would have
// happened to match.
func TestInvoiceSuppressesMultipleProjectsUnderSameClient(t *testing.T) {
	rows := `[
	  {"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-07-10T09:00:00+00:00"}]},
	  {"project_id":2,"hourly_rate_in_cents":8000,"billable_amount_in_cents":80000,"currency":"EUR","time_entries":[{"seconds":3600,"start":"2026-07-11T09:00:00+00:00"}]}
	]`
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return rows, "", "" },
		projects: `[{"id":1,"name":"Alpha","client_id":42},{"id":2,"name":"Beta","client_id":42}]`,
	}
	client := b.transport()
	trk := &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
		Personal: PersonalParams{
			EmployerRate: 0.1892, EmployeeRate: 0.1378,
			MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10,
		},
	}
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{ClientID: 42, Number: "0001", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 500000},
	})

	july := trk.ComputeMonth(context.Background(), 2026, time.July)
	if len(july.Tracked) != 0 {
		t.Errorf("July Tracked = %+v, want empty — both projects share client 42, both suppressed by the 5 Aug invoice", july.Tracked)
	}
}

// TestUnscopedInvoiceAddsIncomeWithoutSuppressingTrackedHours covers an
// invoice for a recipient with no tracking_client_id (or Toggl not
// configured at all): it must still count as income, but must never
// suppress a real client's tracked hours since it isn't scoped to any one
// client.
func TestUnscopedInvoiceAddsIncomeWithoutSuppressingTrackedHours(t *testing.T) {
	rows := `[{"project_id":1,"hourly_rate_in_cents":7500,"billable_amount_in_cents":150000,"currency":"EUR","time_entries":[{"seconds":7200,"start":"2026-07-10T09:00:00+00:00"}]}]`
	b := &fakeBackend{
		detailed: func(page int) (string, string, string) { return rows, "", "" },
		projects: `[{"id":1,"name":"Alpha","client_id":1}]`,
	}
	client := b.transport()
	trk := &Tracker{
		Toggl:       &Toggl{WorkspaceID: "ws", HTTP: client},
		Holidays:    &Holidays{HTTP: client},
		HoursPerDay: 8,
		Loc:         time.UTC,
		Personal: PersonalParams{
			EmployerRate: 0.1892, EmployeeRate: 0.1378,
			MaxInsurableMonthly: 2112, IncomeTaxRate: 0.10,
		},
	}
	trk.Invoiced = ComputeInvoiced([]InvoicedFact{
		{ClientID: UnscopedClientID, Number: "0002", IssueDate: date(2026, 8, 5), DueDate: date(2026, 8, 19), TotalCents: 200000},
	})

	july := trk.ComputeMonth(context.Background(), 2026, time.July)
	if len(july.Tracked) != 1 || july.Tracked[0].AmountCents != 150000 {
		t.Errorf("July Tracked = %+v, want the real tracked hours untouched by the unscoped invoice", july.Tracked)
	}

	september := trk.ComputeMonth(context.Background(), 2026, time.September)
	if len(september.Invoiced) != 1 || september.Invoiced[0].AmountCents != 200000 || september.Invoiced[0].Number != "0002" {
		t.Fatalf("September Invoiced = %+v, want the unscoped invoice's 200000 under number 0002", september.Invoiced)
	}
}

// TestComputeSpendableOnlySetWhenHasHours is a direct unit test (bypassing
// ComputeMonth's real-wall-clock-dependent computeExpected) for the
// hasHours gate: SpendableLabel/URL describe the hours-based portion of
// Income specifically, and must stay empty for a period funded purely by
// invoices.
func TestComputeSpendableOnlySetWhenHasHours(t *testing.T) {
	var withHours Figures
	withHours.computeSpendable(1, 2026, date(2026, 7, 1), true)
	if withHours.SpendableLabel == "" || withHours.SpendableURL == "" {
		t.Errorf("hasHours=true: SpendableLabel/URL = %q/%q, want both set", withHours.SpendableLabel, withHours.SpendableURL)
	}

	var noHours Figures
	noHours.computeSpendable(1, 2026, date(2026, 7, 1), false)
	if noHours.SpendableLabel != "" || noHours.SpendableURL != "" {
		t.Errorf("hasHours=false: SpendableLabel/URL = %q/%q, want both empty (pure-invoice period)", noHours.SpendableLabel, noHours.SpendableURL)
	}
}

func TestEvictMonthAndYear(t *testing.T) {
	trk := fullTracker()
	// A month compute now fetches (and caches) the whole year.
	trk.ComputeMonth(context.Background(), 2026, time.March)
	if trk.Toggl.YearFetchedAt(2026).IsZero() {
		t.Fatal("2026 should be cached after a month compute")
	}
	// Evicting any month in the year drops the shared yearly cache.
	trk.EvictMonth(2026, time.March)
	if !trk.Toggl.YearFetchedAt(2026).IsZero() {
		t.Error("EvictMonth should drop the 2026 cache")
	}

	trk.ComputeYear(context.Background(), 2026)
	if trk.Toggl.YearFetchedAt(2026).IsZero() {
		t.Fatal("2026 should be cached after compute")
	}
	trk.EvictYear(2026)
	if !trk.Toggl.YearFetchedAt(2026).IsZero() {
		t.Error("EvictYear should drop the 2026 cache")
	}
}
