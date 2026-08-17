package main

import (
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/buildinfo"
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
	// Which data checkout is mounted. Only the deployment knows — the data is
	// bind-mounted at run time, not baked into the image — so it comes in
	// through the environment. Neither is required, and neither is validated:
	// this is a line in the header, and an app that refuses to start over a
	// cosmetic variable would be the wrong trade.
	buildinfo.Data = buildinfo.DataStamp{
		UpdatedAt: os.Getenv("DATA_UPDATED_AT"),
		Commit:    os.Getenv("DATA_COMMIT"),
	}

	if err := requireKnownEnv(c.env); err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}
	applyDefaults(&c)
	if c.env == envProd {
		if err := requireProdVars(c); err != nil {
			log.Fatalf("pocketcfo: %v", err)
		}
	} else {
		log.Printf("pocketcfo: ENV=%s — login is DISABLED and every request is served as admin, "+
			"including the client-portal links. Never expose this to a network you do not control.", c.env)
	}
	return c
}

const (
	envProd        = "prod"
	envDevelopment = "development"
)

func requireKnownEnv(env string) error {
	switch env {
	case envProd, envDevelopment:
		return nil
	case "":
		return fmt.Errorf("ENV is not set — use ENV=%s to serve with login, or ENV=%s to run locally without it (see .envrc.example)",
			envProd, envDevelopment)
	default:
		return fmt.Errorf("ENV=%q is not an environment — use %s or %s (see .envrc.example)",
			env, envProd, envDevelopment)
	}
}

func applyDefaults(c *config) {
	if c.repo == "" {
		c.repo = "unset"
	}
	if c.port == "" {
		c.port = "8080"
	}
}

const minSecretLength = 32

func requireProdVars(c config) error {
	required := map[string]string{
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
	for _, name := range slices.Sorted(maps.Keys(required)) {
		if required[name] == "" {
			return fmt.Errorf("%s is not set (see .envrc.example)", name)
		}
	}

	if !strings.HasPrefix(c.baseURL, "https://") {
		return fmt.Errorf("PUBLIC_BASE_URL=%q must be https:// in %s — session cookies and the OAuth redirect are derived from it",
			c.baseURL, envProd)
	}

	secrets := map[string]string{
		"SESSION_SECRET":     c.sessionSecret,
		"CLIENT_LINK_SECRET": c.clientLinkSecret,
		"OTP_LINK_SECRET":    c.otpLinkSecret,
	}
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		if len(secrets[name]) < minSecretLength {
			return fmt.Errorf("%s is only %d characters — use at least %d (try `openssl rand -hex 32`)",
				name, len(secrets[name]), minSecretLength)
		}
	}
	return nil
}
