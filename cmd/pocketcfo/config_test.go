package main

import (
	"net/http"
	"reflect"
	"testing"

	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func TestApplyDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   config
		want config
	}{
		{
			name: "empty config fills in every default",
			in:   config{},
			want: config{repo: "unset", port: "8080"},
		},
		{
			name: "existing values are left alone",
			in:   config{env: "development", repo: "acme/data", port: "9090"},
			want: config{env: "development", repo: "acme/data", port: "9090"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.in
			applyDefaults(&c)
			if !reflect.DeepEqual(c, tt.want) {
				t.Errorf("applyDefaults(%+v) = %+v, want %+v", tt.in, c, tt.want)
			}
		})
	}
}

func TestRequireKnownEnv(t *testing.T) {
	for _, env := range []string{"prod", "development"} {
		if err := requireKnownEnv(env); err != nil {
			t.Errorf("requireKnownEnv(%q) = %v, want nil", env, err)
		}
	}
	for _, env := range []string{"", "production", "Prod", "PROD", "dev", "staging"} {
		if err := requireKnownEnv(env); err == nil {
			t.Errorf("requireKnownEnv(%q) = nil, want an error", env)
		}
	}
}

func prodConfig() config {
	const secret = "0123456789abcdef0123456789abcdef"
	return config{
		clientID: "id", clientSecret: "secret", sessionSecret: secret,
		clientLinkSecret: secret, baseURL: "https://example.com",
		repo: "acme/data", sesRegion: "eu-west-1", sesFromEmail: "a@b.com",
		otpLinkSecret: secret,
	}
}

func TestRequireProdVars(t *testing.T) {
	t.Setenv("GITHUB_REPO", "acme/data")

	t.Run("a complete config passes", func(t *testing.T) {
		if err := requireProdVars(prodConfig()); err != nil {
			t.Errorf("requireProdVars = %v, want nil", err)
		}
	})

	t.Run("a missing var is refused", func(t *testing.T) {
		c := prodConfig()
		c.sesFromEmail = ""
		if err := requireProdVars(c); err == nil {
			t.Error("want an error for a missing SES_FROM_EMAIL")
		}
	})

	t.Run("an http base URL is refused", func(t *testing.T) {
		c := prodConfig()
		c.baseURL = "http://example.com"
		if err := requireProdVars(c); err == nil {
			t.Error("want an error for a plaintext PUBLIC_BASE_URL")
		}
	})

	t.Run("a short secret is refused", func(t *testing.T) {
		for _, name := range []string{"session", "clientLink", "otp"} {
			c := prodConfig()
			switch name {
			case "session":
				c.sessionSecret = "hunter2"
			case "clientLink":
				c.clientLinkSecret = "hunter2"
			case "otp":
				c.otpLinkSecret = "hunter2"
			}
			if err := requireProdVars(c); err == nil {
				t.Errorf("want an error for a short %s secret", name)
			}
		}
	})
}

func TestLoadConfig_DevelopmentDefaults(t *testing.T) {
	t.Chdir(t.TempDir()) // no config.json here - LoadFileConfig degrades to a zero value
	t.Setenv("ENV", "development")
	t.Setenv("GITHUB_REPO", "")
	t.Setenv("PORT", "")

	c := loadConfig()
	if c.env != "development" {
		t.Errorf("env = %q, want development", c.env)
	}
	if c.repo != "unset" {
		t.Errorf("repo = %q, want unset", c.repo)
	}
	if c.port != "8080" {
		t.Errorf("port = %q, want 8080", c.port)
	}
}

func TestBuildTracker(t *testing.T) {
	t.Run("toggl disabled without credentials", func(t *testing.T) {
		trk := buildTracker(financeconfig.Config{}, &http.Client{}, "data")
		if trk.Toggl != nil {
			t.Error("want Toggl nil when TogglToken/TogglWorkspace are unset")
		}
	})
	t.Run("toggl enabled with credentials", func(t *testing.T) {
		trk := buildTracker(financeconfig.Config{TogglToken: "tok", TogglWorkspace: "ws"}, &http.Client{}, "data")
		tg, ok := trk.Toggl.(*tracker.Toggl)
		if !ok || tg == nil {
			t.Fatal("want a Toggl client when both credentials are set")
		}
		if tg.Token != "tok" || tg.WorkspaceID != "ws" {
			t.Errorf("Toggl = %+v, want Token=tok WorkspaceID=ws", tg)
		}
	})
	t.Run("budget always configured", func(t *testing.T) {
		trk := buildTracker(financeconfig.Config{}, &http.Client{}, "data")
		if trk.Budget == nil {
			t.Error("want Budget always non-nil (backed by budgetDir)")
		}
	})
}
