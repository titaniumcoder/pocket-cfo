// Command pocketcfo is a GitHub-OAuth-gated web app combining a finance
// tracker (predictions off a configured hourly rate, real invoiced income,
// a hand-maintained expense budget) at "/" with a read-only invoicing
// viewer at "/invoicing". See ARCHITECTURE.md §8. It assumes it is run from
// the repo root (or, in the deployed image, from wherever the Dockerfile
// copied web/, data/, and build/ to) — same convention as invoicectl.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
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
)

const (
	sessionCookie = "invoicer_session"
	stateCookie   = "invoicer_oauth_state"
	stateTTL      = 10 * time.Minute
)

// recipientsDir, invoicesDir, and buildDir default to the layout described in
// ARCHITECTURE.md §2, overridable via RECIPIENTS_DIR/INVOICES_DIR/BUILD_DIR —
// e.g. to point this binary at a data checkout that lives outside its own
// repo. renderManifestPath is duplicated as a separate var in
// cmd/invoicectl/render.go (a different `main` package) rather than shared,
// matching the existing precedent of buildDir/invoicesDir being independently
// declared per-binary. templatesDir and staticDir default to this repo's own
// branding — overridable via TEMPLATES_DIR/STATIC_DIR for a deployment that
// wants its own look instead. TEMPLATES_DIR is shared with internal/render,
// which reads invoice.html.tmpl from the same directory.
// usersFile is the private, per-deployment access-control list (see
// internal/users and schemas/users.json) — email → which part(s) of
// PocketCFO that non-collaborator may reach. GitHub collaborators bypass it
// entirely (see (*server).authorized); it's only consulted for the
// email-OTP tier below.
var (
	recipientsDir      = getenv("RECIPIENTS_DIR", "data/recipients")
	invoicesDir        = getenv("INVOICES_DIR", "data/invoices")
	buildDir           = getenv("BUILD_DIR", "build")
	renderManifestPath = buildDir + "/render-manifest.json"
	templatesDir       = getenv("TEMPLATES_DIR", "web/templates")
	staticDir          = getenv("STATIC_DIR", "web/static")
	usersFile          = getenv("USERS_FILE", "data/users.json")
	// budgetDir holds budget.json for the finance tracker (see buildTracker)
	// — same override convention as the vars above.
	budgetDir = getenv("BUDGET_DIR", "data")
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type config struct {
	env              string
	clientID         string
	clientSecret     string
	sessionSecret    string
	clientLinkSecret string
	baseURL          string
	repo             string
	port             string
	sesRegion        string
	sesFromEmail     string
	otpLinkSecret    string

	// finance holds the finance tracker's own settings (config.json +
	// TOGGL_*/API_PASSWORD env vars) — see internal/finance/config. Not
	// fail-fast like the rest of config: Toggl/the JSON API degrade to
	// "disabled" rather than refusing to start, since the finance part
	// should stay usable with minimal setup.
	finance financeconfig.Config
}

func loadConfig() config {
	financeFileConfig, err := financeconfig.LoadFileConfig(getenv("CONFIG_FILE", "config.json"))
	if err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}

	c := config{
		env:              os.Getenv("ENV"),
		clientID:         os.Getenv("GITHUB_OAUTH_CLIENT_ID"),
		clientSecret:     os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"),
		sessionSecret:    os.Getenv("SESSION_SECRET"),
		clientLinkSecret: os.Getenv("CLIENT_LINK_SECRET"),
		baseURL:          strings.TrimSuffix(os.Getenv("PUBLIC_BASE_URL"), "/"),
		repo:             os.Getenv("GITHUB_REPO"),
		port:             os.Getenv("PORT"),
		sesRegion:        os.Getenv("AWS_REGION"),
		sesFromEmail:     os.Getenv("SES_FROM_EMAIL"),
		otpLinkSecret:    os.Getenv("OTP_LINK_SECRET"),
		finance:          financeconfig.Load(financeFileConfig),
	}
	if c.env == "" {
		c.env = "development"
	}
	if c.repo == "" {
		c.repo = "titaniumcoder/pocket-cfo"
	}
	if c.port == "" {
		c.port = "8080"
	}

	// GitHub OAuth and the email-login path are only enforced in prod (see
	// (*server).currentSession) — local development skips both entirely, so
	// none of this is required unless ENV=prod.
	if c.env == "prod" {
		missing := map[string]string{
			"GITHUB_OAUTH_CLIENT_ID":     c.clientID,
			"GITHUB_OAUTH_CLIENT_SECRET": c.clientSecret,
			"SESSION_SECRET":             c.sessionSecret,
			"CLIENT_LINK_SECRET":         c.clientLinkSecret,
			"PUBLIC_BASE_URL":            c.baseURL,
			"AWS_REGION":                 c.sesRegion,
			"SES_FROM_EMAIL":             c.sesFromEmail,
			"OTP_LINK_SECRET":            c.otpLinkSecret,
		}
		for name, v := range missing {
			if v == "" {
				log.Fatalf("invoicer: %s is not set (see .envrc.example)", name)
			}
		}
	}
	return c
}

type server struct {
	cfg            config
	httpClient     *http.Client
	indexTmpl      *template.Template
	loginTmpl      *template.Template
	clientTmpl     *template.Template
	emailLoginTmpl *template.Template
	emailSentTmpl  *template.Template

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
// runtime (BUDGET_DIR, default "data") — a real directory, not embedded, so
// a volume mount at that path (or RECIPIENTS_DIR/INVOICES_DIR/USERS_FILE's
// containing directory) can swap in real data without a rebuild. Invoiced
// is left nil here — trackerForRequest fills it in fresh per request from
// real invoice data.
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
		Holidays:     &tracker.Holidays{Subdivision: cfg.Subdivision, HTTP: httpClient},
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
		indexTmpl:        template.Must(template.New("index.html").Funcs(templateFuncs).ParseFiles(templatesDir + "/index.html")),
		loginTmpl:        template.Must(template.ParseFiles(templatesDir + "/login.html")),
		clientTmpl:       template.Must(template.New("client.html").Funcs(templateFuncs).ParseFiles(templatesDir + "/client.html")),
		emailLoginTmpl:   template.Must(template.ParseFiles(templatesDir + "/email_login.html")),
		emailSentTmpl:    template.Must(template.ParseFiles(templatesDir + "/email_sent.html")),
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

	// Finance tracker: the landing page. Toggl/Budget data is all served
	// from in-memory cache after the first fetch (see
	// Tracker.EvictMonth/EvictYear, the Reload link), so there's no
	// meaningfully slow path here worth a separate loading state for.
	mux.HandleFunc("GET /{$}", s.financeCurrentMonth)
	mux.HandleFunc("GET /{year}", s.financeYear)
	mux.HandleFunc("GET /{year}/{month}", s.financeMonth)
	mux.HandleFunc("GET /api/net-income/{year}", s.financeAPIAuth(s.financeAPINetIncomeYear))
	mux.HandleFunc("GET /api/net-income/{year}/{month}", s.financeAPIAuth(s.financeAPINetIncomeMonth))

	addr := ":" + cfg.port
	log.Printf("pocketcfo listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
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
}

func (s *server) isProd() bool {
	return s.cfg.env == "prod"
}

// currentSession is the single point every auth-gated handler goes
// through. Outside of prod it short-circuits to a synthetic
// always-authorized session — GitHub OAuth is only enforced when
// ENV=prod, so local development never needs a registered OAuth App or a
// browser login. See ARCHITECTURE.md §8 and the ENV=prod note in README.md.
func (s *server) currentSession(r *http.Request) (auth.Session, bool) {
	if !s.isProd() {
		return auth.Session{Login: "local-dev", Permission: "admin"}, true
	}

	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return auth.Session{}, false
	}
	sess, err := auth.Decode(s.cfg.sessionSecret, c.Value)
	if err != nil {
		return auth.Session{}, false
	}
	return sess, true
}

// authorized is the full/write-eligible tier: GitHub collaborators with
// push or admin on the data repo. See readOnly and authenticated for the
// email-login tier alongside it.
func (s *server) authorized(sess auth.Session) bool {
	return sess.Permission == "push" || sess.Permission == "admin"
}

// readOnly is the email-login tier — see handleEmailLoginCallback. It grants
// dashboard viewing but not the portal-link column (see portalLinks), which
// stays exclusive to authorized().
func (s *server) readOnly(sess auth.Session) bool {
	return sess.Permission == "readonly"
}

// authenticated is either tier — the gate for handleIndex/handleInvoicePDF,
// which show the same read-only dashboard content to both.
func (s *server) authenticated(sess auth.Session) bool {
	return s.authorized(sess) || s.readOnly(sess)
}

// handleIndex is the invoicing dashboard: unauthenticated visitors get a
// bare login prompt; authenticated ones with no access to the invoicing
// part (a users.json-listed, email-OTP session scoped to finance only) get
// bounced to "/" instead — "users with access to both get shared links,
// others land to wherever they have access to" (PocketCFO plan §5.2).
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
	if !sess.HasPart(users.PartInvoicing) {
		if sess.HasPart(users.PartFinance) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		http.Error(w, "you don't have access to invoicing", http.StatusForbidden)
		return
	}

	recipients, err := stats.LoadRecipients(recipientsDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	invoices, err := stats.LoadInvoices(invoicesDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Portal links are bearer secrets for the client-portal tier (see
	// client.go) — only the full authorized() tier gets to see them; the
	// email-login readOnly tier gets everything else on the dashboard.
	var portalLinks map[int]string
	if s.authorized(sess) {
		portalLinks = s.portalLinks(recipients)
	}

	view := struct {
		Login           string
		ReadOnly        bool
		ShowFinanceLink bool
		Years           []int
		SelectedYear    *int
		Recipients      []stats.RecipientRow
		Invoices        []stats.InvoiceRow
		PortalLinks     map[int]string
		PDFCurrent      map[string]bool
	}{
		Login:           sess.Login,
		ReadOnly:        s.readOnly(sess),
		ShowFinanceLink: sess.HasPart(users.PartFinance),
		Years:           years,
		SelectedYear:    selectedYear,
		Recipients:      recipientRows,
		Invoices:        invoiceRows,
		PortalLinks:     portalLinks,
		PDFCurrent:      pdfCurrentMap(invoices),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.indexTmpl.Execute(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// pdfCurrentMap builds the admin-only "does the built PDF still match the
// current JSON" indicator per invoice number (see ARCHITECTURE.md §5.1's
// staleness rules and internal/render/staleness.go). The reference hash was
// precomputed by invoicectl render; this only does the cheap current-side
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

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(stateTTL),
	})
	redirectURI := s.cfg.baseURL + "/auth/callback"
	http.Redirect(w, r, auth.AuthorizeURL(s.cfg.clientID, redirectURI, state), http.StatusFound)
}

func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookieVal, err := r.Cookie(stateCookie)
	if err != nil || stateCookieVal.Value == "" || stateCookieVal.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid or missing OAuth state", http.StatusBadRequest)
		return
	}
	clearCookie(w, stateCookie, s.secureCookies())

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	token, err := auth.ExchangeCode(ctx, s.httpClient, s.cfg.clientID, s.cfg.clientSecret, code)
	if err != nil {
		http.Error(w, "GitHub login failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	login, err := auth.CurrentUser(ctx, s.httpClient, token)
	if err != nil {
		http.Error(w, "GitHub login failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	permission, err := auth.CollaboratorPermission(ctx, s.httpClient, token, s.cfg.repo, login)
	if err != nil {
		http.Error(w, "GitHub permission check failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	sess := auth.NewSession(login, permission, auth.TTL)
	if !s.authorized(sess) {
		http.Error(w, fmt.Sprintf("forbidden: %s does not have write access to %s", login, s.cfg.repo), http.StatusForbidden)
		return
	}

	encoded, err := auth.Encode(s.cfg.sessionSecret, sess)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(auth.TTL),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, sessionCookie, s.secureCookies())
	http.Redirect(w, r, "/", http.StatusFound)
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

func (s *server) secureCookies() bool {
	return strings.HasPrefix(s.cfg.baseURL, "https://")
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
