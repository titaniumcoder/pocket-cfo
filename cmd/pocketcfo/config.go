package main

import (
	"log"
	"os"
	"strings"

	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
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
		// Harmless in dev/test: currentSession bypasses GitHub auth
		// entirely outside prod, so c.repo is never actually consulted.
		// Deliberately NOT defaulted to titaniumcoder/pocket-cfo (the
		// public code repo) — GITHUB_REPO is required below in prod so a
		// real deployment can't silently check collaborator permission
		// against the wrong repo (it should be the private data repo,
		// e.g. titaniumcoder/pocket-cfo-data — see that repo's own
		// docker-compose.yml).
		c.repo = "unset"
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
	return c
}
