package main

import (
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/buildinfo"
	"github.com/titaniumcoder/pocket-cfo/internal/chat"
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
	togglCacheDir      = os.Getenv("TOGGL_CACHE_DIR")
)

// catalogNotesPath is read per call rather than at init, so tests can point
// CATALOG_DIR at a fixture catalog with t.Setenv.
func catalogNotesPath() string {
	return filepath.Join(getenv("CATALOG_DIR", "catalog"), "notes.json")
}

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

	hermesAPIToken  string
	githubDataToken string
	githubAPIURL    string

	openAIKey       string
	openAIBaseURL   string
	openAIModel     string
	openAIExtraBody string
	chatDir         string

	finance   financeconfig.Config
	togglMode string
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
		hermesAPIToken:   os.Getenv("HERMES_API_TOKEN"),
		githubDataToken:  os.Getenv("GITHUB_DATA_TOKEN"),
		githubAPIURL:     os.Getenv("GITHUB_API_URL"),
		openAIKey:        os.Getenv("OPENAI_API_KEY"),
		openAIBaseURL:    os.Getenv("OPENAI_BASE_URL"),
		openAIModel:      os.Getenv("OPENAI_MODEL"),
		openAIExtraBody:  os.Getenv("OPENAI_EXTRA_BODY"),
		chatDir:          os.Getenv("CHAT_DIR"),
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
	mode, err := resolveTogglMode(c.finance)
	if err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}
	c.togglMode = mode
	applyDefaults(&c)
	if err := resolveChat(&c, togglCacheDir); err != nil {
		log.Fatalf("pocketcfo: %v", err)
	}
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

const (
	togglModeTrack = "track"
	togglModeFocus = "toggl2"
	togglModeBoth  = "both"
)

func resolveTogglMode(f financeconfig.Config) (string, error) {
	track := f.TogglToken != "" && f.TogglWorkspace != ""
	focus := toggl2Complete(f)
	if f.Toggl2Key != "" && !focus {
		log.Printf("pocketcfo: TOGGL2_API_KEY is set without TOGGL2_ORGANIZATION_ID and TOGGL2_WORKSPACE_ID — Toggl 2.0 stays off the dashboard; /info shows the ids the key can see")
	}
	switch f.TogglMode {
	case "":
		switch {
		case track && focus:
			log.Printf("pocketcfo: TOGGL_MODE is unset with both TOGGL_API_TOKEN and TOGGL2_API_KEY present — staying on Toggl Track; set TOGGL_MODE=%s or %s to change that", togglModeFocus, togglModeBoth)
			return togglModeTrack, nil
		case track:
			return togglModeTrack, nil
		case focus:
			return togglModeFocus, nil
		}
		return "", nil
	case togglModeTrack:
		if !track {
			return "", fmt.Errorf("TOGGL_MODE=%s needs TOGGL_API_TOKEN and TOGGL_WORKSPACE_ID", togglModeTrack)
		}
	case togglModeFocus:
		if !focus {
			return "", fmt.Errorf("TOGGL_MODE=%s needs TOGGL2_API_KEY, TOGGL2_ORGANIZATION_ID and TOGGL2_WORKSPACE_ID — with only the key set, /info shows the ids it can see", togglModeFocus)
		}
	case togglModeBoth:
		if !track || !focus {
			return "", fmt.Errorf("TOGGL_MODE=%s needs TOGGL_API_TOKEN, TOGGL_WORKSPACE_ID, TOGGL2_API_KEY, TOGGL2_ORGANIZATION_ID and TOGGL2_WORKSPACE_ID — with only the 2.0 key set, /info shows the ids it can see", togglModeBoth)
		}
	default:
		return "", fmt.Errorf("TOGGL_MODE=%q is not a mode — use %s, %s or %s, or leave it unset with one set of Toggl credentials", f.TogglMode, togglModeTrack, togglModeFocus, togglModeBoth)
	}
	return f.TogglMode, nil
}

func (c config) chatEnabled() bool { return c.openAIKey != "" }

func resolveChat(c *config, cacheDir string) error {
	if !c.chatEnabled() {
		return nil
	}
	if c.openAIModel == "" {
		return fmt.Errorf("OPENAI_API_KEY is set but OPENAI_MODEL is not — name the model (or OpenRouter preset) the chat should use (see .envrc.example)")
	}
	if _, err := chat.ParseExtraBody(c.openAIExtraBody); err != nil {
		return fmt.Errorf("OPENAI_EXTRA_BODY %v", err)
	}
	if c.openAIBaseURL == "" {
		c.openAIBaseURL = chat.DefaultBaseURL
	}
	if c.chatDir == "" && cacheDir != "" {
		c.chatDir = filepath.Join(cacheDir, "chats")
	}
	if c.chatDir == "" {
		return fmt.Errorf("OPENAI_API_KEY is set but there is nowhere to keep chats — set CHAT_DIR, or TOGGL_CACHE_DIR to put them beside the Toggl cache (see .envrc.example)")
	}
	return nil
}

func toggl2Complete(f financeconfig.Config) bool {
	return f.Toggl2Key != "" && f.Toggl2Organization != "" && f.Toggl2Workspace != ""
}

func togglModeLabel(mode string) tracker.Mode {
	switch mode {
	case togglModeTrack:
		return tracker.ModeTrack
	case togglModeFocus:
		return tracker.ModeFocus
	case togglModeBoth:
		return tracker.ModeBoth
	}
	return tracker.ModeOff
}

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
