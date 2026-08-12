package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

// buildInvoicedFacts is the bridge between the invoicing schema
// (internal/schema/invoice, recipient) and the finance tracker's own
// schema-decoupled tracker.InvoicedFact (see
// internal/finance/tracker/invoiced.go): every *issued* invoice becomes one
// fact. A recipient's tracking_client_id links the invoice to that Toggl
// client's tracked/predicted hours (suppressing them in favor of the real
// invoiced total); a recipient with no tracking_client_id — or Toggl not
// configured at all — still contributes its invoice as income, just under
// tracker.UnscopedClientID, which never suppresses anything (see
// invoiced.go). Read fresh on every request — same "no caching, re-read the
// checkout" convention handleIndex's own stats.LoadRecipients/LoadInvoices
// calls already use — so issuing a real invoice shows up in the finance
// view immediately, no restart required.
func buildInvoicedFacts() ([]tracker.InvoicedFact, error) {
	recipients, err := stats.LoadRecipients(recipientsDir)
	if err != nil {
		return nil, err
	}
	invoices, err := stats.LoadInvoices(invoicesDir)
	if err != nil {
		return nil, err
	}

	clientIDByRecipient := map[int]int{}
	for _, r := range recipients {
		if r.TrackingClientId != nil {
			clientIDByRecipient[r.Number] = *r.TrackingClientId
		}
	}

	var facts []tracker.InvoicedFact
	for _, inv := range invoices {
		if inv.Status != invoice.InvoiceJsonStatusIssued {
			continue // drafts aren't real invoices yet, see PocketCFO plan §2.3
		}
		cid := tracker.UnscopedClientID
		if mapped, ok := clientIDByRecipient[inv.Recipient.Number]; ok {
			cid = mapped
		}
		totals, err := money.Compute(inv)
		if err != nil {
			continue // a malformed invoice shouldn't take down the whole dashboard
		}
		facts = append(facts, tracker.InvoicedFact{
			ClientID:   cid,
			Number:     inv.Number,
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
		s.rememberDestination(w, r)
		tracker.RenderLogin(w, "", s.emailLoginAvailable())
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

// fillInvoiceLinks sets each Invoiced row's PDF URL, but only when
// showInvoicingLink is true (the viewing session has invoicing rights) — a
// finance-only viewer still sees each invoice's bare number and amount
// (fair information for them to have), just not a link to the PDF itself.
// Split out from renderFinancePage so this gating logic is testable without
// needing the HTML template to also support it.
func fillInvoiceLinks(invoiced []tracker.InvoicedRow, showInvoicingLink bool) {
	if !showInvoicingLink {
		return
	}
	for i, row := range invoiced {
		invoiced[i].URL = "/invoicing/invoices/" + row.Number + ".pdf"
	}
}

// renderFinancePage fills in the session-derived presentation fields
// (see Figures' doc comment) that compute() itself can't know about, then
// renders the page. ShowInfoLink mirrors s.authorized(sess) — the same gate
// handleInfo itself enforces — so an email-OTP session never sees a link to
// a page it can't reach.
func (s *server) renderFinancePage(w http.ResponseWriter, sess auth.Session, f tracker.Figures) {
	f.Login = sess.Login
	f.ReadOnly = sess.Permission == "readonly"
	f.ShowInvoicingLink = sess.HasPart(users.PartInvoicing)
	f.ShowInfoLink = s.authorized(sess)
	// The drill-down carries statement descriptions, so only admins get a
	// link; everyone else sees the figures as plain text. The menu entry
	// follows the month being viewed; the per-row links need actuals to
	// point at, and a month rather than a year.
	if s.showSpending(sess) {
		f.ShowSpendingLink = true
		if f.ShowActuals && f.Mode == "month" {
			f.SpendingDetailURL = f.MonthViewURL + "/spending"
		}
	}
	fillInvoiceLinks(f.Invoiced, f.ShowInvoicingLink)
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
	s.renderFinancePage(w, sess, trk.ComputeMonth(ctx, now.Year(), now.Month()))
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
	year, ok := s.parseYear(w, r, trk.Loc)
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
	s.renderFinancePage(w, sess, trk.ComputeYear(ctx, year))
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
	year, month, ok := s.parseYearMonth(w, r, trk.Loc)
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
	s.renderFinancePage(w, sess, trk.ComputeMonth(ctx, year, time.Month(month)))
}

// The viewable range comes from tracker.NavBounds, the same function the
// pickers use. It used to be a second yearRange constant here that happened to
// agree with the tracker's — which is exactly how a URL check and a month
// picker come to disagree about which months exist.
func (s *server) parseYear(w http.ResponseWriter, r *http.Request, loc *time.Location) (int, bool) {
	now := time.Now().In(loc)
	minYear, maxYear := tracker.NavBounds(now, s.budgetStart())
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < minYear || year > maxYear {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return 0, false
	}
	return year, true
}

func (s *server) parseYearMonth(w http.ResponseWriter, r *http.Request, loc *time.Location) (int, int, bool) {
	now := time.Now().In(loc)
	year, err1 := strconv.Atoi(r.PathValue("year"))
	month, err2 := strconv.Atoi(r.PathValue("month"))
	if err1 != nil || err2 != nil || !tracker.MonthIsOffered(year, month, now, s.budgetStart()) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return 0, 0, false
	}
	return year, month, true
}

// budgetStart is the configured first budgeted month, zero when unset.
func (s *server) budgetStart() time.Time { return s.cfg.finance.StartMonth }

// isRefresh reports whether the request came from the Reload button (?refresh=1).
func isRefresh(r *http.Request) bool {
	return r.URL.Query().Get("refresh") != ""
}

// isMinimalToggle reports whether the request came from the minimal-budget
// switch (?minimal=toggle).
func isMinimalToggle(r *http.Request) bool {
	return r.URL.Query().Get("minimal") == "toggle"
}

// financeSpending renders the admin-only drill-down, gated like /info:
// anonymous visitors log in first, authenticated non-admins are refused. The
// 403 isn't what protects the descriptions — Figures never carries one.
func (s *server) financeSpending(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		s.rememberDestination(w, r)
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return
	}
	if !s.authorized(sess) {
		http.Error(w, "you don't have access to this page", http.StatusForbidden)
		return
	}

	trk, err := s.trackerForRequest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	year, month, ok := s.parseYearMonth(w, r, trk.Loc)
	if !ok {
		return
	}
	if isRefresh(r) {
		trk.EvictMonth(year, time.Month(month))
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	v := trk.ComputeSpending(ctx, year, time.Month(month))
	v.Header = s.header(sess, webui.PageSpending, webui.Period{Year: year, Month: month})
	tracker.RenderSpending(w, v)
}
