package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
)

// newFinanceTestServer builds a server with a real (network-free) Tracker:
// recipients/invoices dirs exist but are empty. The HTTP client's transport
// answers the holidays API with an empty (but valid) result instead of
// reaching the real openholidaysapi.org - Toggl stays nil (no credentials
// configured), so it never makes a request at all; anything unexpected
// fails the test outright rather than silently hitting the network.
func newFinanceTestServer(t *testing.T) *server {
	t.Helper()
	t.Chdir(t.TempDir())
	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)

	noNetwork := &http.Client{Transport: oauthRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "openholidaysapi.org") {
			return jsonResp(http.StatusOK, `[]`), nil
		}
		t.Fatalf("unexpected request in a network-free finance test: %s %s", r.Method, r.URL)
		return nil, nil
	})}

	return &server{
		cfg:        config{env: "prod", sessionSecret: "test-secret"},
		httpClient: noNetwork,
		tracker:    buildTracker(financeconfig.Config{}, noNetwork, "data"),
	}
}

func readOnlyRequest(t *testing.T, s *server, path string, parts []string) *http.Request {
	t.Helper()
	encoded, err := auth.Encode(s.cfg.sessionSecret, auth.NewReadOnlySession("someone@example.com", parts, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: encoded})
	return r
}

// TestBuildInvoicedFacts_LinkedRecipientProducesClientScopedFact confirms an
// issued invoice for a recipient with tracking_client_id set becomes a fact
// scoped to that Toggl client, carrying the invoice's own Number through.
func TestBuildInvoicedFacts_LinkedRecipientProducesClientScopedFact(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)

	clientID := 424242
	writeJSON(t, recipientsDir+"/0001.json", recipient.RecipientJson{
		Number: 1, LegalName: "Alice Ltd", Email: "alice@example.com", IsBusiness: true,
		Language: recipient.RecipientJsonLanguageDe, PaymentTermsDays: 14,
		Address:          recipient.Address{Line1: "Street 1", PostalCode: "1000", City: "Vienna", CountryCode: "AT"},
		TrackingClientId: &clientID,
	})
	writeJSON(t, invoicesDir+"/INV-0000000001.json", invoiceFixture("INV-0000000001", 1, invoice.InvoiceJsonStatusIssued))

	facts, err := buildInvoicedFacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want exactly 1", facts)
	}
	if facts[0].ClientID != clientID {
		t.Errorf("ClientID = %d, want %d", facts[0].ClientID, clientID)
	}
	if facts[0].Number != "INV-0000000001" {
		t.Errorf("Number = %q, want INV-0000000001", facts[0].Number)
	}
}

// TestBuildInvoicedFacts_UnlinkedRecipientStillProducesUnscopedFact is the
// regression test for Bug A: an invoice for a recipient with no
// tracking_client_id (or, identically, no matching recipient file at all —
// covered here since the fixture has no recipients directory entries) must
// still produce a fact, just scoped to tracker.UnscopedClientID, so it
// still counts as income without silently vanishing.
func TestBuildInvoicedFacts_UnlinkedRecipientStillProducesUnscopedFact(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)

	writeJSON(t, invoicesDir+"/INV-0000000001.json", invoiceFixture("INV-0000000001", 1, invoice.InvoiceJsonStatusIssued))

	facts, err := buildInvoicedFacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want exactly 1", facts)
	}
	if facts[0].ClientID != tracker.UnscopedClientID {
		t.Errorf("ClientID = %d, want UnscopedClientID (%d)", facts[0].ClientID, tracker.UnscopedClientID)
	}
}

// TestBuildInvoicedFacts_DraftInvoicesSkipped confirms drafts still never
// become facts, unaffected by the ClientID/Number rewrite.
func TestBuildInvoicedFacts_DraftInvoicesSkipped(t *testing.T) {
	t.Chdir(t.TempDir())
	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)

	writeJSON(t, invoicesDir+"/INV-0000000002.json", invoiceFixture("INV-0000000002", 1, invoice.InvoiceJsonStatusDraft))

	facts, err := buildInvoicedFacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Errorf("facts = %+v, want none (draft invoices aren't real yet)", facts)
	}
}

// TestFillInvoiceLinks_GatedOnInvoicingRights confirms invoice PDF links are
// only filled in when the viewing session has invoicing rights — a
// finance-only viewer keeps each invoice's bare number/amount (fair
// information for them to have) but gets no URL to the PDF itself.
func TestFillInvoiceLinks_GatedOnInvoicingRights(t *testing.T) {
	withoutRights := []tracker.InvoicedRow{{Number: "INV-0000000001", AmountCents: 500000}}
	fillInvoiceLinks(withoutRights, false)
	if withoutRights[0].URL != "" {
		t.Errorf("URL = %q, want empty when showInvoicingLink is false", withoutRights[0].URL)
	}

	withRights := []tracker.InvoicedRow{{Number: "INV-0000000001", AmountCents: 500000}}
	fillInvoiceLinks(withRights, true)
	if want := "/invoicing/invoices/INV-0000000001.pdf"; withRights[0].URL != want {
		t.Errorf("URL = %q, want %q", withRights[0].URL, want)
	}
}

func TestFinanceSession_InvoicingOnlyRedirectsToInvoicing(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := readOnlyRequest(t, s, "/", []string{users.PartInvoicing})
	w := httptest.NewRecorder()

	_, ok := s.financeSession(w, r)
	if ok {
		t.Fatal("want ok=false for an invoicing-only session on a finance route")
	}
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/invoicing" {
		t.Errorf("want redirect to /invoicing, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestFinanceSession_FinanceOnlyPasses(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance})
	w := httptest.NewRecorder()

	sess, ok := s.financeSession(w, r)
	if !ok {
		t.Fatalf("want ok=true for a finance-scoped session, got response %d", w.Code)
	}
	if sess.Login != "someone@example.com" {
		t.Errorf("Login = %q, want someone@example.com", sess.Login)
	}
}

func TestFinanceSession_NoPartsIsForbidden(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := readOnlyRequest(t, s, "/", nil)
	w := httptest.NewRecorder()

	if _, ok := s.financeSession(w, r); ok {
		t.Fatal("want ok=false for a session with no parts at all")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestFinanceSession_UnauthenticatedShowsLogin(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	r := httptest.NewRequest(http.MethodGet, "/", nil) // no cookie
	w := httptest.NewRecorder()

	if _, ok := s.financeSession(w, r); ok {
		t.Fatal("want ok=false with no session at all")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (the login page itself)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PocketCFO") {
		t.Error("want the login page body, got something else")
	}
}

// TestLoginPageOffersEmailWhenUsersExist is the regression test for a real
// bug: financeSession passed showEmailLogin=false unconditionally, so the
// "Continue with email" option never appeared on the start page at all —
// email login was unreachable no matter what users.json said. The option
// still hides when nobody is listed, since it could only dead-end there.
func TestLoginPageOffersEmailWhenUsersExist(t *testing.T) {
	s := &server{cfg: config{env: "prod", sessionSecret: "test-secret"}}
	t.Chdir(t.TempDir())
	mustMkdirAll(t, filepath.Dir(usersFile))

	render := func() string {
		r := httptest.NewRequest(http.MethodGet, "/", nil) // no cookie
		w := httptest.NewRecorder()
		s.financeSession(w, r)
		return w.Body.String()
	}

	mustWriteFile(t, usersFile, `{"users":[]}`)
	if body := render(); strings.Contains(body, "/auth/email") {
		t.Error("with nobody listed in users.json, the email option must stay hidden")
	}

	mustWriteFile(t, usersFile, `{"users":[{"email":"person@example.com","parts":["finance"]}]}`)
	body := render()
	if !strings.Contains(body, `href="/auth/email"`) {
		t.Errorf("with a listed user, the login page must offer email login; got: %s", body)
	}
	if !strings.Contains(body, `href="/auth/login"`) {
		t.Error("the GitHub option must still be there too")
	}
}

func TestHandleIndex_FinanceOnlyRedirectsToRoot(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	r := readOnlyRequest(t, s, "/invoicing", []string{users.PartFinance})
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("want redirect to /, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestHandleIndex_NoPartsIsForbidden(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	r := readOnlyRequest(t, s, "/invoicing", nil)
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestFinanceCurrentMonth_RendersForAuthorizedSession(t *testing.T) {
	s := newFinanceTestServer(t)
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance})
	w := httptest.NewRecorder()

	s.financeCurrentMonth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "PocketCFO") {
		t.Error("want the finance dashboard body")
	}
}

// TestFinanceCurrentMonth_SinglePartSessionHasNoNav confirms the shared
// header's topnav is omitted entirely when this session has nowhere else
// to go — a finance-only session viewing the finance dashboard has no
// Invoicing or Info link to show, so a menu bar with nothing but the
// current (active) page would be pointless chrome.
func TestFinanceCurrentMonth_SinglePartSessionHasNoNav(t *testing.T) {
	s := newFinanceTestServer(t)
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance})
	w := httptest.NewRecorder()

	s.financeCurrentMonth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `class="topnav"`) {
		t.Error("finance-only session has nowhere else to go — want no topnav at all")
	}
}

// TestFinanceCurrentMonth_BothPartsSessionShowsInvoicingNavButNotInfo
// confirms a readonly session with both finance and invoicing access sees
// the Invoicing link (something to navigate to), but never an Info link —
// /info is authorized()-only, and a readonly session must never even see a
// link to a page it can't reach.
func TestFinanceCurrentMonth_BothPartsSessionShowsInvoicingNavButNotInfo(t *testing.T) {
	s := newFinanceTestServer(t)
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance, users.PartInvoicing})
	w := httptest.NewRecorder()

	s.financeCurrentMonth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="/invoicing"`) {
		t.Error("a session with both parts should see the Invoicing nav link")
	}
	if strings.Contains(body, `href="/info"`) {
		t.Error("a readonly session must never see the Info nav link")
	}
}

func TestFinanceYear_InvalidYearRedirects(t *testing.T) {
	s := newFinanceTestServer(t)
	r := readOnlyRequest(t, s, "/1900", []string{users.PartFinance})
	r.SetPathValue("year", "1900")
	w := httptest.NewRecorder()

	s.financeYear(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("want redirect to / for an out-of-range year, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestFinanceMonth_ValidYearMonthRenders(t *testing.T) {
	s := newFinanceTestServer(t)
	now := time.Now()
	r := readOnlyRequest(t, s, "/", []string{users.PartFinance})
	r.SetPathValue("year", strconv.Itoa(now.Year()))
	r.SetPathValue("month", strconv.Itoa(int(now.Month())))
	w := httptest.NewRecorder()

	s.financeMonth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
}

func TestIsRefreshAndIsMinimalToggle(t *testing.T) {
	tests := []struct {
		name              string
		query             string
		wantRefresh       bool
		wantMinimalToggle bool
	}{
		{"neither", "", false, false},
		{"refresh", "?refresh=1", true, false},
		{"minimal toggle", "?minimal=toggle", false, true},
		{"minimal wrong value", "?minimal=other", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/"+tt.query, nil)
			if got := isRefresh(r); got != tt.wantRefresh {
				t.Errorf("isRefresh = %v, want %v", got, tt.wantRefresh)
			}
			if got := isMinimalToggle(r); got != tt.wantMinimalToggle {
				t.Errorf("isMinimalToggle = %v, want %v", got, tt.wantMinimalToggle)
			}
		})
	}
}

func TestHandleInvoicePDF_ServesExistingFile(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing/invoices/INV-0000000001.pdf", []string{users.PartInvoicing})
	r.SetPathValue("file", "INV-0000000001.pdf")
	w := httptest.NewRecorder()

	s.handleInvoicePDF(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "pdf-1" {
		t.Errorf("body = %q, want pdf-1", w.Body.String())
	}
}

func TestHandleInvoicePDF_RejectsPathTraversal(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing/invoices/traversal.pdf", []string{users.PartInvoicing})
	r.SetPathValue("file", "../secret.pdf")
	w := httptest.NewRecorder()

	s.handleInvoicePDF(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a path-traversal attempt", w.Code)
	}
}

func TestHandleInvoicePDF_UnauthenticatedRedirectsToLogin(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	r := httptest.NewRequest(http.MethodGet, "/invoicing/invoices/INV-0000000001.pdf", nil)
	r.SetPathValue("file", "INV-0000000001.pdf")
	w := httptest.NewRecorder()

	s.handleInvoicePDF(w, r)

	if w.Code != http.StatusFound || w.Header().Get("Location") != "/auth/login" {
		t.Errorf("want redirect to /auth/login, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestHandleIndex_AuthorizedRendersInvoicingDashboard(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing", []string{users.PartInvoicing})
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	// Unlike the client portal (scoped by a recipient's own token), the
	// admin invoicing dashboard lists every recipient's invoices, including
	// drafts (see stats.LoadInvoices).
	body := w.Body.String()
	for _, number := range []string{"INV-0000000001", "INV-0000000002", "INV-0000000003"} {
		if !strings.Contains(body, number) {
			t.Errorf("%s missing from the invoicing dashboard", number)
		}
	}
}

// TestHandleIndex_InvoicingOnlySessionHasNoNav mirrors
// TestFinanceCurrentMonth_SinglePartSessionHasNoNav on the invoicing side:
// an invoicing-only session has nowhere else to go, so no topnav at all.
func TestHandleIndex_InvoicingOnlySessionHasNoNav(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing", []string{users.PartInvoicing})
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `class="topnav"`) {
		t.Error("invoicing-only session has nowhere else to go — want no topnav at all")
	}
}

// TestHandleIndex_BothPartsSessionShowsFinanceNavButNotInfo is the
// invoicing-side mirror of
// TestFinanceCurrentMonth_BothPartsSessionShowsInvoicingNavButNotInfo.
func TestHandleIndex_BothPartsSessionShowsFinanceNavButNotInfo(t *testing.T) {
	s := newTestClientServer(t)
	s.cfg.env = "prod"
	s.cfg.sessionSecret = "test-secret"
	t.Chdir(t.TempDir())
	writeFixtures(t)

	r := readOnlyRequest(t, s, "/invoicing", []string{users.PartFinance, users.PartInvoicing})
	w := httptest.NewRecorder()

	s.handleIndex(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="/"`) {
		t.Error("a session with both parts should see the Finance nav link")
	}
	if strings.Contains(body, `href="/info"`) {
		t.Error("a readonly session must never see the Info nav link")
	}
}
