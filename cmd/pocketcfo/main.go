package main

import (
	"context"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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

	tracker *tracker.Tracker

	emailRequestMu   sync.Mutex
	emailRequestedAt map[string]time.Time
}

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
		Actuals:      &tracker.Actuals{FS: os.DirFS(budgetDir)},
		HoursPerDay:  cfg.HoursPerDay,
		Loc:          time.Local,
		VacationDays: cfg.AnnualVacationDays,
		RateCents:    cfg.HourlyRateCents,
		RateCurrency: cfg.Currency,
		Personal:     tracker.PersonalParams{Legislation: cfg.Legislation, Salary: cfg.Salary},
		Start:        cfg.StartMonth,
	}
}

const togglTimeout = 60 * time.Second

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

	warmCtx, stopWarming := context.WithCancel(context.Background())
	defer stopWarming()
	go s.tracker.Warm(warmCtx, togglRefreshInterval())

	mux := s.routes()

	addr := ":" + cfg.port
	log.Printf("pocketcfo listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func mustPageTemplate(path string) *template.Template {
	t := template.Must(template.New(filepath.Base(path)).Funcs(templateFuncs).Parse(webui.HeaderTemplate))
	return template.Must(t.ParseFiles(path))
}

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
	"amount": func(v float64) string {
		return render.FormatAmount(int64(math.Round(v * 100)))
	},
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/invoicing" {
		http.NotFound(w, r)
		return
	}

	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		s.rememberDestination(w, r)
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

func (s *server) checkInvoicingAccess(sess auth.Session) (redirect string, forbidden bool) {
	if s.authenticatedForPart(sess, users.PartInvoicing) {
		return "", false
	}
	if sess.HasPart(users.PartFinance) {
		return "/", false
	}
	return "", true
}

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
	if selectedYear != nil && !slices.Contains(years, *selectedYear) {
		selectedYear = nil
		_, recipientRows, invoiceRows, err = stats.Aggregate(invoices, recipients, paid, nil, time.Now())
		if err != nil {
			return nil, err
		}
	}

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
		Header:       s.header(sess, webui.PageInvoicing, webui.ParsePeriod(r.URL.Query().Get("year"), r.URL.Query().Get("month"))),
		Years:        years,
		SelectedYear: selectedYear,
		Recipients:   recipientRows,
		Invoices:     invoiceRows,
		PortalLinks:  portalLinks,
		PDFCurrent:   pdfCurrentMap(invoices),
	}, nil
}

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

func (s *server) handleInvoicePDF(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.currentSession(r)
	if !ok || !s.authenticated(sess) {
		s.rememberDestination(w, r)
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

func (s *server) financeMonthSub(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("action") != "spending" {
		http.NotFound(w, r)
		return
	}
	s.financeSpending(w, r)
}

func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if file == "" || strings.Contains(file, "/") || strings.Contains(file, "..") {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(staticDir, file)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/{file}", s.handleStatic)
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

	mux.HandleFunc("GET /{$}", s.financeCurrentMonth)
	mux.HandleFunc("GET /{year}", s.financeYear)
	mux.HandleFunc("GET /{year}/{month}", s.financeMonth)
	mux.HandleFunc("GET /{year}/{month}/{action}", s.financeMonthSub)

	s.registerAPI(mux)

	return mux
}
