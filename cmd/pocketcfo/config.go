package main

import (
	"log"
	"os"
	"strings"
	"time"

	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

// dataDir is the override point for hand-edited data, e.g. to point at a data
// checkout outside this repo (ARCHITECTURE.md §2). buildDir is separate because
// rendered PDFs are generated output, not hand-edited data; renderManifestPath
// is declared independently in cmd/pocket-cfo-ctl too, per the existing
// per-binary precedent. usersFile is the access-control list consulted only for
// the email-OTP tier — GitHub collaborators bypass it (see (*server).authorized).
var (
	dataDir            = getenv("DATA_DIR", "data")
	recipientsDir      = dataDir + "/recipients"
	invoicesDir        = dataDir + "/invoices"
	paidInvoicesPath   = dataDir + "/paid-invoices.json"
	usersFile          = dataDir + "/users.json"
	buildDir           = getenv("BUILD_DIR", "build")
	renderManifestPath = buildDir + "/render-manifest.json"
	templatesDir       = getenv("TEMPLATES_DIR", "templates")
	staticDir          = getenv("STATIC_DIR", "static")
	// Same directory as the rest of dataDir's hand-edited data.
	budgetDir = dataDir
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// togglRefreshInterval reads TOGGL_REFRESH_INTERVAL as a Go duration, e.g.
// "5m" (see tracker.Tracker.Warm). A bad value warns and falls back rather than
// failing startup — a typo in a tuning knob shouldn't take the app down.
func togglRefreshInterval() time.Duration {
	raw := os.Getenv("TOGGL_REFRESH_INTERVAL")
	if raw == "" {
		return tracker.DefaultWarmInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("TOGGL_REFRESH_INTERVAL=%q is not a positive duration; using %s", raw, tracker.DefaultWarmInterval)
		return tracker.DefaultWarmInterval
	}
	return d
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
	// Optional: /info's account-balance section is omitted when unset.
	// pocket-cfo-ctl reads the same env var independently for PDF rendering.
	api2pdfKey string

	// Optional: absent means the Hermes routes are never registered at all.
	// Deliberately not in requireProdVars — same convention as api2pdfKey.
	hermesAPIToken  string
	githubDataToken string
	// Optional, defaults to https://api.github.com. Points the Contents
	// client somewhere else — a scratch instance, or a stub while verifying
	// the write path by hand.
	githubAPIURL string

	// Not fail-fast like the rest of config: Toggl and the JSON API degrade to
	// disabled rather than refusing to start, so the finance part stays usable
	// with minimal setup.
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
		api2pdfKey:       os.Getenv("API2PDF_KEY"),
		hermesAPIToken:   os.Getenv("HERMES_API_TOKEN"),
		githubDataToken:  os.Getenv("GITHUB_DATA_TOKEN"),
		githubAPIURL:     os.Getenv("GITHUB_API_URL"),
		finance:          financeconfig.Load(financeFileConfig),
	}
	applyDevDefaults(&c)
	if c.env == "prod" {
		requireProdVars(c)
	}
	return c
}

// applyDevDefaults fills in the fallbacks that only matter outside prod,
// where currentSession bypasses GitHub auth entirely, so these values are
// never actually consulted.
func applyDevDefaults(c *config) {
	if c.env == "" {
		c.env = "development"
	}
	if c.repo == "" {
		// Deliberately NOT defaulted to the public code repo: GITHUB_REPO is
		// required in prod so a deployment can't silently check collaborator
		// permission against the wrong one. It should be the private data repo.
		c.repo = "unset"
	}
	if c.port == "" {
		c.port = "8080"
	}
}

// requireProdVars fails fast on missing config in prod. GitHub OAuth and the
// email-login path are only enforced in prod (see (*server).currentSession)
// — local development skips both entirely, so none of this is required
// unless ENV=prod.
func requireProdVars(c config) {
	missing := map[string]string{
		"GITHUB_OAUTH_CLIENT_ID":     c.clientID,
		"GITHUB_OAUTH_CLIENT_SECRET": c.clientSecret,
		"SESSION_SECRET":             c.sessionSecret,
		"CLIENT_LINK_SECRET":         c.clientLinkSecret,
		"PUBLIC_BASE_URL":            c.baseURL,
		"GITHUB_REPO":                os.Getenv("GITHUB_REPO"),
		"AWS_REGION":                 c.sesRegion,
		"SES_FROM_EMAIL":             c.sesFromEmail,
		"OTP_LINK_SECRET":            c.otpLinkSecret,
	}
	for name, v := range missing {
		if v == "" {
			log.Fatalf("pocketcfo: %s is not set (see .envrc.example)", name)
		}
	}
}
