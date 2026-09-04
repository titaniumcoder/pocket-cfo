package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

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
			continue
		}
		cid := tracker.UnscopedClientID
		if mapped, ok := clientIDByRecipient[inv.Recipient.Number]; ok {
			cid = mapped
		}
		totals, err := money.Compute(inv)
		if err != nil {
			continue
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

func (s *server) trackerForRequest() (tracker.Tracker, error) {
	trk := *s.tracker
	facts, err := buildInvoicedFacts()
	if err != nil {
		return trk, err
	}
	trk.Invoiced = tracker.ComputeInvoiced(facts)
	return trk, nil
}

func (s *server) trackerForView(r *http.Request) (tracker.Tracker, error) {
	trk, err := s.trackerForRequest()
	trk.Minimal = minimalFor(r)
	return trk, err
}

func (s *server) authenticatedForPart(sess auth.Session, part string) bool {
	return s.authenticated(sess) && sess.HasPart(part)
}

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

func fillInvoiceLinks(invoiced []tracker.InvoicedRow, showInvoicingLink bool) {
	if !showInvoicingLink {
		return
	}
	for i, row := range invoiced {
		invoiced[i].URL = "/invoicing/invoices/" + row.Number + ".pdf"
	}
}

func (s *server) renderFinancePage(w http.ResponseWriter, sess auth.Session, f tracker.Figures) {
	f.Login = sess.Login
	f.ReadOnly = sess.Permission == "readonly"
	f.ShowInvoicingLink = sess.HasPart(users.PartInvoicing)
	f.ShowInfoLink = s.authorized(sess)
	f.ShowChatLink = s.chatVisible(sess)
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
	trk, err := s.trackerForView(r)
	if err != nil {
		serverError(w, r, "loading data", err)
		return
	}
	now := time.Now().In(trk.Loc)
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	s.renderFinancePage(w, sess, trk.ComputeMonth(ctx, now.Year(), now.Month()))
}

func (s *server) financeYear(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.financeSession(w, r)
	if !ok {
		return
	}
	trk, err := s.trackerForView(r)
	if err != nil {
		serverError(w, r, "loading data", err)
		return
	}
	year, ok := s.parseYear(w, r, trk.Loc)
	if !ok {
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
	trk, err := s.trackerForView(r)
	if err != nil {
		serverError(w, r, "loading data", err)
		return
	}
	year, month, ok := s.parseYearMonth(w, r, trk.Loc)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	s.renderFinancePage(w, sess, trk.ComputeMonth(ctx, year, time.Month(month)))
}

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

func (s *server) budgetStart() time.Time { return s.cfg.finance.StartMonth }

const minimalCookie = "pocketcfo_minimal"

func minimalFor(r *http.Request) bool {
	c, err := r.Cookie(minimalCookie)
	return err == nil && c.Value == "1"
}

func (s *server) handleMinimalToggle(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.financeSession(w, r); !ok {
		return
	}
	value := "1"
	if minimalFor(r) {
		value = ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: minimalCookie, Value: value, Path: "/",
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, backTo(r), http.StatusSeeOther)
}

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.financeSession(w, r)
	if !ok {
		return
	}
	_ = sess
	trk, err := s.trackerForView(r)
	if err != nil {
		serverError(w, r, "loading data", err)
		return
	}
	now := time.Now().In(trk.Loc)
	year, month := now.Year(), now.Month()
	if y, err := strconv.Atoi(r.FormValue("year")); err == nil {
		year = y
	}
	if m, err := strconv.Atoi(r.FormValue("month")); err == nil && m >= 1 && m <= 12 {
		month = time.Month(m)
	}
	if r.FormValue("year") != "" && r.FormValue("month") == "" {
		trk.EvictYear(year)
	} else {
		trk.EvictMonth(year, month)
	}
	http.Redirect(w, r, backTo(r), http.StatusSeeOther)
}

func backTo(r *http.Request) string {
	to := r.FormValue("return")
	if strings.HasPrefix(to, "/") && !strings.HasPrefix(to, "//") {
		return to
	}
	return "/"
}

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

	trk, err := s.trackerForView(r)
	if err != nil {
		serverError(w, r, "loading data", err)
		return
	}
	year, month, ok := s.parseYearMonth(w, r, trk.Loc)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	v := trk.ComputeSpending(ctx, year, time.Month(month))
	v.Header = s.header(sess, webui.PageSpending, webui.Period{Year: year, Month: month})
	tracker.RenderSpending(w, v)
}
