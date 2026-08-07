// Command pocketcfo is a GitHub-OAuth-gated web app combining a finance
// tracker (predictions off a configured hourly rate, real invoiced income,
// a hand-maintained expense budget) at "/" with a read-only invoicing
// viewer at "/invoicing". See ARCHITECTURE.md §8. It assumes it is run from
// the repo root (or, in the deployed image, from wherever the Dockerfile
// copied web/, data/, and build/ to) — same convention as pocket-cfo-ctl.
package main

import (
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
	cfg            config
	httpClient     *http.Client
	indexTmpl      *template.Template
	loginTmpl      *template.Template
	clientTmpl     *template.Template
	emailLoginTmpl *template.Template
	emailSentTmpl  *template.Template
	infoTmpl       *template.Template

	// tracker is the finance part's shared, cached-in-memory core (Toggl/
	// Holidays/Budget caches, config-sourced rate) — see
	// trackerForRequest, which shallow-copies it per request to rebuild
	// just the Invoiced field from the latest invoice data.
	tracker *tracker.Tracker

	// emailRequestedAt backs allowEmailRequest's per-address cooldown — not
	// persisted, so it resets on restart, which is fine for a soft throttle.
	emailRequestMu   sync.Mutex
	emailRequestedAt map[string]time.Time
}

// buildTracker wires the finance tracker's core from cfg.finance — Toggl
// stays nil (the tracked-hours layer disabled) when TOGGL_API_TOKEN/
// TOGGL_WORKSPACE_ID aren't set, per PocketCFO's "Toggl is optional,
// config-toggled" plan; Budget reads budget.json fresh from budgetDir at
// runtime (DATA_DIR, default "data") — a real directory, not embedded, so a
// volume mount at that path can swap in real data (recipients/, invoices/,
// users.json, budget.json all together) without a rebuild. Invoiced is left
// nil here — trackerForRequest fills it in fresh per request from real
// invoice data.
func buildTracker(cfg financeconfig.Config, httpClient *http.Client, budgetDir string) *tracker.Tracker {
	var togglClient *tracker.Toggl
	if cfg.TogglToken != "" && cfg.TogglWorkspace != "" {
		togglClient = &tracker.Toggl{
			Token:       cfg.TogglToken,
			WorkspaceID: cfg.TogglWorkspace,
			ProjectIDs:  cfg.TogglProjects,
			HTTP:        httpClient,
		}
	}
	return &tracker.Tracker{
		Toggl:        togglClient,
		Holidays:     &tracker.Holidays{Country: cfg.Country, Subdivision: cfg.Subdivision, HTTP: httpClient},
		Budget:       &tracker.Budget{FS: os.DirFS(budgetDir)},
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

func main() {
	cfg := loadConfig()

	httpClient := &http.Client{Timeout: 15 * time.Second}
	s := &server{
		cfg:              cfg,
		httpClient:       httpClient,
		tracker:          buildTracker(cfg.finance, httpClient, budgetDir),
		indexTmpl:        mustPageTemplate(templatesDir + "/index.html"),
		loginTmpl:        template.Must(template.ParseFiles(templatesDir + "/login.html")),
		clientTmpl:       template.Must(template.New("client.html").Funcs(templateFuncs).ParseFiles(templatesDir + "/client.html")),
		emailLoginTmpl:   template.Must(template.ParseFiles(templatesDir + "/email_login.html")),
		emailSentTmpl:    template.Must(template.ParseFiles(templatesDir + "/email_sent.html")),
		infoTmpl:         mustPageTemplate(templatesDir + "/info.html"),
		emailRequestedAt: map[string]time.Time{},
	}

	mux := http.NewServeMux()
	// Under /invoicing/ (not the bare /static/ this had before the finance
	// tracker's /{year}/{month} routes landed at root) -- Go's ServeMux
	// can't disambiguate a generic two-segment wildcard from an unrelated
	// subtree pattern at the same root, so invoicing's own static assets
	// move under its own path prefix like everything else in Phase 5a.
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

	// Finance tracker: the landing page. Toggl/Budget data is all served
	// from in-memory cache after the first fetch (see
	// Tracker.EvictMonth/EvictYear, the Reload link), so there's no
	// meaningfully slow path here worth a separate loading state for.
	mux.HandleFunc("GET /{$}", s.financeCurrentMonth)
	mux.HandleFunc("GET /{year}", s.financeYear)
	mux.HandleFunc("GET /{year}/{month}", s.financeMonth)

	addr := ":" + cfg.port
	log.Printf("pocketcfo listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// mustPageTemplate parses the full-page template at path with the shared
// site header (see internal/webui) already defined, so the page can invoke
// {{template "sitehead" .Header}} instead of hand-rolling its own header
// markup — the finance dashboard's own template set does the same, which is
// what keeps the three pages' chrome identical. Takes a full path rather
// than a bare name so tests can resolve templates/ absolutely before
// chdir'ing into a fixture directory, and still build them exactly the way
// main does.
func mustPageTemplate(path string) *template.Template {
	t := template.Must(template.New(filepath.Base(path)).Funcs(templateFuncs).Parse(webui.HeaderTemplate))
	return template.Must(t.ParseFiles(path))
}

// templateFuncs reuses internal/render's exact Bulgarian-format money and
// date rendering, so the dashboard matches the PDFs instead of a second
// implementation.
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
	// amount renders a plain float (the api2pdf balance, which arrives as
	// a JSON number, not minor units) to two decimals in the same
	// Bulgarian/European convention as every other figure in the app.
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.loginTmpl.Execute(w, nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
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

// checkInvoicingAccess reports how an already-authenticated sess may reach
// the invoicing dashboard: a users.json-listed, email-OTP session scoped to
// finance only redirects to "/" instead — "users with access to both get
// shared links, others land to wherever they have access to" (PocketCFO
// plan §5.2) — and a session with neither part is forbidden outright.
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

	var selectedYear *int
	if raw := r.URL.Query().Get("year"); raw != "" {
		y, err := strconv.Atoi(raw)
		if err == nil {
			selectedYear = &y
		}
	}

	years, recipientRows, invoiceRows, err := stats.Aggregate(invoices, recipients, selectedYear, time.Now())
	if err != nil {
		return nil, err
	}

	// Portal links are bearer secrets for the client-portal tier (see
	// client.go) — only the full authorized() tier gets to see them; the
	// email-login readOnly tier gets everything else on the dashboard.
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

// pdfCurrentMap builds the admin-only "does the built PDF still match the
// current JSON" indicator per invoice number (see ARCHITECTURE.md §5.1's
// staleness rules and internal/render/staleness.go). The reference hash was
// precomputed by pocket-cfo-ctl render; this only does the cheap current-side
// comparison, never touching api2pdf. Missing/unloadable manifest is
// treated as "nothing recorded yet" rather than a hard error — the
// dashboard must still load.
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
