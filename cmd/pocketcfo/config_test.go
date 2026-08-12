package main

import (
	"net/http"
	"reflect"
	"testing"

	financeconfig "github.com/titaniumcoder/pocket-cfo/internal/finance/config"
)

func TestApplyDevDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   config
		want config
	}{
		{
			name: "empty env fills in every default",
			in:   config{},
			want: config{env: "development", repo: "unset", port: "8080"},
		},
		{
			name: "existing values are left alone",
			in:   config{env: "staging", repo: "acme/data", port: "9090"},
			want: config{env: "staging", repo: "acme/data", port: "9090"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.in
			applyDevDefaults(&c)
			if !reflect.DeepEqual(c, tt.want) {
				t.Errorf("applyDevDefaults(%+v) = %+v, want %+v", tt.in, c, tt.want)
			}
		})
	}
}

func TestRequireProdVars_AllPresentDoesNotExit(t *testing.T) {
	t.Setenv("GITHUB_REPO", "acme/data")
	c := config{
		clientID: "id", clientSecret: "secret", sessionSecret: "sess",
		clientLinkSecret: "link", baseURL: "https://example.com",
		repo: "acme/data", sesRegion: "eu-west-1", sesFromEmail: "a@b.com",
		otpLinkSecret: "otp",
	}
	// requireProdVars calls log.Fatalf (os.Exit) on any missing var - simply
	// returning here without the process dying is the test.
	requireProdVars(c)
}

func TestLoadConfig_DevelopmentDefaults(t *testing.T) {
	t.Chdir(t.TempDir()) // no config.json here - LoadFileConfig degrades to a zero value
	t.Setenv("ENV", "")
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
		if trk.Toggl == nil {
			t.Fatal("want Toggl non-nil when both credentials are set")
		}
		if trk.Toggl.Token != "tok" || trk.Toggl.WorkspaceID != "ws" {
			t.Errorf("Toggl = %+v, want Token=tok WorkspaceID=ws", trk.Toggl)
		}
	})
	t.Run("budget always configured", func(t *testing.T) {
		trk := buildTracker(financeconfig.Config{}, &http.Client{}, "data")
		if trk.Budget == nil {
			t.Error("want Budget always non-nil (backed by budgetDir)")
		}
	})
}
