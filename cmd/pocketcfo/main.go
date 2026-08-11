// Command pocketcfo is a GitHub-OAuth-gated web app: a finance tracker at "/"
// and a read-only invoicing viewer at "/invoicing". See ARCHITECTURE.md §8.
// Run from the repo root, or wherever the Dockerfile copied the data to.
package main

import (
	"context"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/auth"
	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
	"github.com/titaniumcoder/pocket-cfo/internal/money"
	"github.com/titaniumcoder/pocket-cfo/internal/render"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
	"github.com/titaniumcoder/pocket-cfo/internal/users"
	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

type server struct {
	cfg        config
	httpClient *http.Client
	indexTmpl  *template.Template
	clientTmpl *template.Template
	infoTmpl   *template.Template

	// Shared and cached in memory; trackerForRequest shallow-copies it per
	// request to rebuild just the Invoiced field.
	tracker *tracker.Tracker

	// Backs allowEmailRequest's cooldown. Not persisted; a soft throttle can
	// afford to reset on restart.
	emailRequestMu   sync.Mutex
	emailRequestedAt map[string]time.Time
}

// buildTracker wires the tracker's core from cfg.finance. Toggl stays nil when
// its credentials aren't set. Budget reads budgetDir at runtime rather than
// embedding, so a volume mount can swap in real data without a rebuild.
// Invoiced is left nil; trackerForRequest fills it per request.
func buildTracker(cfg financeconfig.Config, httpClient *http.Client, budgetDir string) *tracker.Tracker {
	var togglClient *tracker.Toggl
	if cfg.TogglToken != "" && cfg.TogglWorkspace != "" {
		togglClient = &tracker.Toggl{
			Token:       cfg.TogglToken,
			WorkspaceID: cfg.TogglWorkspace,
			ProjectIDs:  cfg.TogglProjects,
			HTTP:        togglHTTPClient(httpClient),
		}
	}
	return &tracker.Tracker{
		Toggl:        togglClient,
		Holidays:     &tracker.Holidays{Country: cfg.Country, Subdivision: cfg.Subdivision, HTTP: httpClient},
		Budget:       &tracker.Budget{FS: os.DirFS(budgetDir)},
		Accounts:     &tracker.Accounts{FS: os.DirFS(budgetDir)},
		HoursPerDay:  cfg.HoursPerDay,
		Loc:          time.Local,
		VacationDays: cfg.AnnualVacationDays,
		RateCents:    cfg.HourlyRateCents,
		RateCurrency: cfg.Currency,
		Personal: tracker.PersonalParams{
			EmployerRate:        cfg.EmployerRate,
			EmployeeRate:        cfg.EmployeeRate,
			MaxInsurableMonthly: cfg.MaxInsurableMonthly,
			IncomeTaxRate:       cfg.IncomeTaxRate,
		},
	}
}

// togglTimeout is the ceiling for one Toggl API call: the Reports v3 detailed
// report pages through several POSTs and is by a wide margin the slowest thing
// this app calls, while SES and the OAuth callback want to fail fast. Sharing
// one 15s client across all four is why the report kept dying with
// "Client.Timeout exceeded while awaiting headers".
const togglTimeout = 60 * time.Second

// togglHTTPClient derives the Toggl client from the shared one, keeping its
// Transport so injected test round-trippers still apply.
func togglHTTPClient(shared *http.Client) *http.Client {
	c := *shared
	c.Timeout = togglTimeout
	return &c
}

func main() {
	cfg := loadConfig()

	httpClient := &http.Client{Timeout: 15 * time.Second}
	s := &server{
		cfg:              cfg,
		httpClient:       httpClient,
		tracker:          buildTracker(cfg.finance, httpClient, budgetDir),
		indexTmpl:        mustPageTemplate(templatesDir + "/index.html"),
		clientTmpl:       template.Must(template.New("client.html").Funcs(templateFuncs).ParseFiles(templatesDir + "/client.html")),
		infoTmpl:         mustPageTemplate(templatesDir + "/info.html"),
		emailRequestedAt: map[string]time.Time{},
	}

	// Started before the listener: on a scale-to-zero host the first request
	// arrives moments after boot, and joining a fetch already in flight beats
	// starting one.
	warmCtx, stopWarming := context.WithCancel(context.Background())
	defer stopWarming()
	go s.tracker.Warm(warmCtx, togglRefreshInterval())

	mux := http.NewServeMux()
	// Under /invoicing/ rather than a bare /static/: ServeMux can't
	// disambiguate a two-segment wildcard from a subtree pattern at the same
	// root, and /{year}/{month} owns the root.
	mux.Handle("GET /invoicing/static/", http.StripPrefix("/invoicing/static/", http.FileServer(http.Dir(staticDir))))
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("GET /auth/logout", s.handleLogout)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /auth/email", s.handleEmailLoginForm)
	mux.HandleFunc("POST /auth/email", s.handleEmailLoginRequest)
	mux.HandleFunc("GET /auth/email/callback", s.handleEmailLoginCallback)
	mux.HandleFunc("GET /invoicing/invoices/{file}", s.handleInvoicePDF)
	mux.HandleFunc("GET /invoicing/client/{token}", s.handleClientPortal)
	mux.HandleFunc("GET /invoicing/client/{token}/invoices/{file}", s.handleClientInvoicePDF)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /invoicing", s.handleIndex)
	mux.HandleFunc("GET /info", s.handleInfo)

	// Finance tracker: the landing page.
	mux.HandleFunc("GET /{$}", s.financeCurrentMonth)
	mux.HandleFunc("GET /{year}", s.financeYear)
	mux.HandleFunc("GET /{year}/{month}", s.financeMonth)

	addr := ":" + cfg.port
	log.Printf("pocketcfo listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// mustPageTemplate parses a page with the shared site header already defined,
// so it can invoke {{template "sitehead" .Header}} rather than hand-rolling
// chrome. Takes a full path so tests can resolve templates/ absolutely before
// chdir'ing into a fixture directory.
func mustPageTemplate(path string) *template.Template {
	t := template.Must(template.New(filepath.Base(path)).Funcs(templateFuncs).Parse(webui.HeaderTemplate))
	return template.Must(t.ParseFiles(path))
}

// templateFuncs reuses internal/render's formatting so the dashboard matches
// the PDFs rather than being a second implementation.
var templateFuncs = template.FuncMap{
	"money": render.FormatMoney,
	"date":  render.FormatDate,
	"dateptr": func(d *types.SerializableDate) string {
		if d == nil {
			return ""
		}
		return render.FormatDate(*d)
	},
	"deref": func(i *int) int {
		if i == nil {
			return 0
		}
		return *i
	},
	// amount renders a plain float (the api2pdf balance arrives as a JSON
	// number, not minor units) in the same convention as every other figure.
	"amount": func(v float64) string {
		return render.FormatAmount(int64(math.Round(v * 100)))
	},
}

// handleIndex is the invoicing dashboard: unauthenticated visitors get a
// bare login prompt; authenticated ones with no access to the invoicing
// part get routed by checkInvoicingAccess.
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/invoicing" {
		http.NotFound(w, r)
		return
	}

	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		tracker.RenderLogin(w, "", s.emailLoginAvailable())
		return
	}
	if redirect, forbidden := s.checkInvoicingAccess(sess); forbidden {
		http.Error(w, "you don't have access to invoicing", http.StatusForbidden)
		return
	} else if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusFound)
		return
	}

	view, err := s.loadInvoicingView(r, sess)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.indexTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// checkInvoicingAccess routes an authenticated session: one scoped to finance
// only is redirected to "/" so a shared link lands where it has access, and one
// with neither part is forbidden.
func (s *server) checkInvoicingAccess(sess auth.Session) (redirect string, forbidden bool) {
	if s.authenticatedForPart(sess, users.PartInvoicing) {
		return "", false
	}
	if sess.HasPart(users.PartFinance) {
		return "/", false
	}
	return "", true
}

// loadInvoicingView loads recipients/invoices, aggregates them for the
// selected year (via the ?year= query param, defaulting to "All"), and
// assembles the invoicing dashboard's template data.
func (s *server) loadInvoicingView(r *http.Request, sess auth.Session) (any, error) {
	recipients, err := stats.LoadRecipients(recipientsDir)
	if err != nil {
		return nil, err
	}
	invoices, err := stats.LoadInvoices(invoicesDir)
	if err != nil {
		return nil, err
	}
	paid, err := stats.LoadPaid(paidInvoicesPath)
	if err != nil {
		return nil, err
	}

	var selectedYear *int
	if raw := r.URL.Query().Get("year"); raw != "" {
		y, err := strconv.Atoi(raw)
		if err == nil {
			selectedYear = &y
		}
	}

	years, recipientRows, invoiceRows, err := stats.Aggregate(invoices, recipients, paid, selectedYear, time.Now())
	if err != nil {
		return nil, err
	}

	// Portal links are bearer secrets, so only the authorized() tier sees
	// them; readOnly gets everything else on the dashboard.
	var portalLinks map[int]string
	if s.authorized(sess) {
		portalLinks = s.portalLinks(recipients)
	}

	return struct {
		Header       webui.Header
		Years        []int
		SelectedYear *int
		Recipients   []stats.RecipientRow
		Invoices     []stats.InvoiceRow
		PortalLinks  map[int]string
		PDFCurrent   map[string]bool
	}{
		Header:       s.header(sess, webui.PageInvoicing),
		Years:        years,
		SelectedYear: selectedYear,
		Recipients:   recipientRows,
		Invoices:     invoiceRows,
		PortalLinks:  portalLinks,
		PDFCurrent:   pdfCurrentMap(invoices),
	}, nil
}

// pdfCurrentMap builds the "does the built PDF still match its JSON" indicator
// per invoice. The reference hash was precomputed by pocket-cfo-ctl render, so
// this is the cheap current-side comparison and never touches api2pdf. An
// unloadable manifest means "nothing recorded yet": the dashboard must load.
func pdfCurrentMap(invoices []*invoice.InvoiceJson) map[string]bool {
	manifest, err := render.LoadManifest(renderManifestPath)
	if err != nil {
		manifest = render.Manifest{}
	}

	current := make(map[string]bool, len(invoices))
	for _, inv := range invoices {
		totals, err := money.Compute(inv)
		if err != nil {
			current[inv.Number] = false
			continue
		}
		current[inv.Number] = render.IsCurrent(inv, totals, manifest)
	}
	return current
}

// handleInvoicePDF serves build/{number}.pdf or build/{number}-paid.pdf.
// One route handles both suffixes rather than two mux patterns, since
// Go's ServeMux wildcards match a whole path segment and can't be mixed
// with a literal suffix within it.
func (s *server) handleInvoicePDF(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		http.Redirect(w, r, "/auth/login", http.StatusFound)
		return
	}

	file := r.PathValue("file")
	if !strings.HasSuffix(file, ".pdf") || strings.Contains(file, "/") || strings.Contains(file, "..") {
		http.NotFound(w, r)
		return
	}
	path := buildDir + "/" + file
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
