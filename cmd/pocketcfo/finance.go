package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

// buildInvoicedFacts is the bridge between the invoicing schema
// (internal/schema/invoice, recipient) and the finance tracker's own
// schema-decoupled tracker.InvoicedFact (see
// internal/finance/tracker/invoiced.go): every *issued* invoice for a
// recipient with a tracking_project_id becomes one fact. Read fresh on
// every request — same "no caching, re-read the checkout" convention
// handleIndex's own stats.LoadRecipients/LoadInvoices calls already use —
// so issuing a real invoice shows up in the finance view immediately, no
// restart required.
func buildInvoicedFacts() ([]tracker.InvoicedFact, error) {
	recipients, err := stats.LoadRecipients(recipientsDir)
	if err != nil {
		return nil, err
	}
	invoices, err := stats.LoadInvoices(invoicesDir)
	if err != nil {
		return nil, err
	}

	projectIDByRecipient := map[int]int{}
	for _, r := range recipients {
		if r.TrackingProjectId != nil {
			projectIDByRecipient[r.Number] = *r.TrackingProjectId
		}
	}

	var facts []tracker.InvoicedFact
	for _, inv := range invoices {
		if inv.Status != invoice.InvoiceJsonStatusIssued {
			continue // drafts aren't real invoices yet, see PocketCFO plan §2.3
		}
		pid, ok := projectIDByRecipient[inv.Recipient.Number]
		if !ok {
			continue // recipient not linked to a tracking-software project
		}
		totals, err := money.Compute(inv)
		if err != nil {
			continue // a malformed invoice shouldn't take down the whole dashboard
		}
		facts = append(facts, tracker.InvoicedFact{
			ProjectID:  pid,
			IssueDate:  inv.IssueDate.Time,
			DueDate:    inv.DueDate.Time,
			TotalCents: int(totals.GrandTotal),
		})
	}
	return facts, nil
}

// trackerForRequest returns a shallow copy of s.tracker with Invoiced
// rebuilt from the current invoice/recipient data on disk. A shallow copy
// is enough: Toggl/Holidays/Budget are shared pointers (their own in-memory
// caches stay intact across requests), only the Invoiced map differs per
// request.
func (s *server) trackerForRequest() (tracker.Tracker, error) {
	trk := *s.tracker
	facts, err := buildInvoicedFacts()
	if err != nil {
		return trk, err
	}
	trk.Invoiced = tracker.ComputeInvoiced(facts)
	return trk, nil
}

// authenticatedForPart is the gate every part-specific route (finance,
// invoicing) goes through: logged in at all, and -- for the readonly
// (email-OTP) tier -- listed in users.json for that part. A push/admin
// session (GitHub collaborator) always passes, via auth.Session.HasPart.
func (s *server) authenticatedForPart(sess auth.Session, part string) bool {
	return s.authenticated(sess) && sess.HasPart(part)
}

// financeSession is the finance routes' shared auth gate: unauthenticated
// visitors get the login page; authenticated ones with no access to
// finance (an invoicing-only readonly session) get bounced to /invoicing
// instead -- the mirror of handleIndex's own redirect the other way (see
// PocketCFO plan §5.2). Returns ok=false when the caller should stop
// (a response was already written).
func (s *server) financeSession(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		tracker.RenderLogin(w, "", false)
		return sess, false
	}
	if s.authenticatedForPart(sess, users.PartFinance) {
		return sess, true
	}
	if sess.HasPart(users.PartInvoicing) {
		http.Redirect(w, r, "/invoicing", http.StatusFound)
		return sess, false
	}
	http.Error(w, "you don't have access to the finance tracker", http.StatusForbidden)
	return sess, false
}

// renderFinancePage fills in the session-derived presentation fields
// (see Figures' doc comment) that compute() itself can't know about, then
// renders the page.
func renderFinancePage(w http.ResponseWriter, sess auth.Session, f tracker.Figures) {
	f.Login = sess.Login
	f.ReadOnly = sess.Permission == "readonly"
	f.ShowInvoicingLink = sess.HasPart(users.PartInvoicing)
	tracker.RenderPage(w, f)
}

const requestTimeout = 20 * time.Second

func (s *server) financeCurrentMonth(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.financeSession(w, r)
	if !ok {
		return
	}
	trk, err := s.trackerForRequest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().In(trk.Loc)
	if isRefresh(r) {
		trk.EvictMonth(now.Year(), now.Month())
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if isMinimalToggle(r) && trk.Budget != nil {
		trk.Budget.ToggleMinimal()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	renderFinancePage(w, sess, trk.ComputeMonth(ctx, now.Year(), now.Month()))
}

func (s *server) financeYear(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.financeSession(w, r)
	if !ok {
		return
	}
	trk, err := s.trackerForRequest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	year, ok := parseYear(w, r, trk.Loc)
	if !ok {
		return
	}
	if isRefresh(r) {
		trk.EvictYear(year)
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	renderFinancePage(w, sess, trk.ComputeYear(ctx, year))
}

func (s *server) financeMonth(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.financeSession(w, r)
	if !ok {
		return
	}
	trk, err := s.trackerForRequest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	year, month, ok := parseYearMonth(w, r, trk.Loc)
	if !ok {
		return
	}
	if isRefresh(r) {
		trk.EvictMonth(year, time.Month(month))
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	if isMinimalToggle(r) && trk.Budget != nil {
		trk.Budget.ToggleMinimal()
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	renderFinancePage(w, sess, trk.ComputeMonth(ctx, year, time.Month(month)))
}

// financeAPIAuth gates GET /api/net-income/... on API_PASSWORD -- a
// separate, simpler credential from the session cookie, since this is
// meant for scripts/API consumers, not browser logins.
func (s *server) financeAPIAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		given := r.Header.Get("X-API-Password")
		if given == "" {
			const prefix = "Bearer "
			if h := r.Header.Get("Authorization"); len(h) > len(prefix) && h[:len(prefix)] == prefix {
				given = h[len(prefix):]
			}
		}
		if s.cfg.finance.APIPassword == "" || subtle.ConstantTimeCompare([]byte(given), []byte(s.cfg.finance.APIPassword)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *server) financeAPINetIncomeYear(w http.ResponseWriter, r *http.Request) {
	trk, err := s.trackerForRequest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	year, ok := parseAPIYear(w, r, trk.Loc)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	writeNetIncome(w, trk.ComputeYear(ctx, year))
}

func (s *server) financeAPINetIncomeMonth(w http.ResponseWriter, r *http.Request) {
	trk, err := s.trackerForRequest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	year, month, ok := parseAPIYearMonth(w, r, trk.Loc)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	writeNetIncome(w, trk.ComputeMonth(ctx, year, time.Month(month)))
}

// writeJSONError writes a JSON error body ({"error": msg}) with the given
// status, so writeNetIncome's error responses stay JSON like its success
// response — this is the app's one JSON API endpoint, and a plain-text
// http.Error body would break that contract for callers parsing errors as
// JSON.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
}

func writeNetIncome(w http.ResponseWriter, f tracker.Figures) {
	if f.Personal.Err != "" {
		writeJSONError(w, http.StatusBadGateway, f.Personal.Err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	resp := struct {
		Mode                        string `json:"mode"`
		Year                        int    `json:"year"`
		Month                       int    `json:"month,omitempty"`
		Currency                    string `json:"currency"`
		ExpectedIncomeCents         int    `json:"expected_income_cents"`
		ExpectedGrossIncomeCents    int    `json:"expected_gross_income_cents"`
		VacationDeductionCents      int    `json:"vacation_deduction_cents,omitempty"`
		ProjectedCompanyIncomeCents int    `json:"projected_company_income_cents"`
		InvoicedCents               int    `json:"invoiced_cents"`
		PersonalIncomeCents         int    `json:"personal_income_cents"`
		EmployerContributionCents   int    `json:"employer_contribution_cents"`
		GrossSalaryCents            int    `json:"gross_salary_cents"`
		EmployeeContributionCents   int    `json:"employee_contribution_cents"`
		PersonalIncomeTaxCents      int    `json:"personal_income_tax_cents"`
	}{
		Mode:                        f.Mode,
		Year:                        f.Year,
		Month:                       f.MonthNum,
		Currency:                    f.Currency,
		ExpectedIncomeCents:         f.ExpectedNetCents,
		ExpectedGrossIncomeCents:    f.ExpectedCents,
		VacationDeductionCents:      f.VacationCentsDeducted,
		ProjectedCompanyIncomeCents: f.TotalCents,
		InvoicedCents:               f.InvoicedCents,
		PersonalIncomeCents:         f.Personal.NetIncomeCents,
		EmployerContributionCents:   f.Personal.EmployerContribCents,
		GrossSalaryCents:            f.Personal.GrossSalaryCents,
		EmployeeContributionCents:   f.Personal.EmployeeContribCents,
		PersonalIncomeTaxCents:      f.Personal.IncomeTaxCents,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
	}
}

const yearRange = 2

func parseYear(w http.ResponseWriter, r *http.Request, loc *time.Location) (int, bool) {
	now := time.Now().In(loc)
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < now.Year()-yearRange || year > now.Year()+yearRange {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return 0, false
	}
	return year, true
}

func parseYearMonth(w http.ResponseWriter, r *http.Request, loc *time.Location) (int, int, bool) {
	now := time.Now().In(loc)
	year, err1 := strconv.Atoi(r.PathValue("year"))
	month, err2 := strconv.Atoi(r.PathValue("month"))
	if err1 != nil || err2 != nil || month < 1 || month > 12 ||
		year < now.Year()-yearRange || year > now.Year()+yearRange {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return 0, 0, false
	}
	return year, month, true
}

func parseAPIYear(w http.ResponseWriter, r *http.Request, loc *time.Location) (int, bool) {
	now := time.Now().In(loc)
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < now.Year()-yearRange || year > now.Year()+yearRange {
		http.Error(w, "invalid year", http.StatusBadRequest)
		return 0, false
	}
	return year, true
}

func parseAPIYearMonth(w http.ResponseWriter, r *http.Request, loc *time.Location) (int, int, bool) {
	now := time.Now().In(loc)
	year, err1 := strconv.Atoi(r.PathValue("year"))
	month, err2 := strconv.Atoi(r.PathValue("month"))
	if err1 != nil || err2 != nil || month < 1 || month > 12 ||
		year < now.Year()-yearRange || year > now.Year()+yearRange {
		http.Error(w, "invalid year or month", http.StatusBadRequest)
		return 0, 0, false
	}
	return year, month, true
}

// isRefresh reports whether the request came from the Reload button (?refresh=1).
func isRefresh(r *http.Request) bool {
	return r.URL.Query().Get("refresh") != ""
}

// isMinimalToggle reports whether the request came from the minimal-budget
// switch (?minimal=toggle).
func isMinimalToggle(r *http.Request) bool {
	return r.URL.Query().Get("minimal") == "toggle"
}
