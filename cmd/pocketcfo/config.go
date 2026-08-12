package main

import (
	"log"
	"os"
	"strings"
	"time"

	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

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
	budgetDir          = dataDir
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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
	api2pdfKey       string

	hermesAPIToken  string
	githubDataToken string
	githubAPIURL    string

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

func applyDevDefaults(c *config) {
	if c.env == "" {
		c.env = "development"
	}
	if c.repo == "" {
		c.repo = "unset"
	}
	if c.port == "" {
		c.port = "8080"
	}
}

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
