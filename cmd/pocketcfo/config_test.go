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

func TestBuildTogglClients(t *testing.T) {
	t.Run("nothing without credentials", func(t *testing.T) {
		track, focus := buildTogglClients(financeconfig.Config{}, &http.Client{})
		if track != nil || focus != nil {
			t.Error("want no clients when no credentials are set")
		}
	})
	t.Run("track from its pair", func(t *testing.T) {
		track, focus := buildTogglClients(financeconfig.Config{TogglToken: "tok", TogglWorkspace: "ws"}, &http.Client{})
		if track == nil || track.Token != "tok" || track.WorkspaceID != "ws" {
			t.Errorf("track = %+v, want Token=tok WorkspaceID=ws", track)
		}
		if focus != nil {
			t.Error("want no 2.0 client without TOGGL2_API_KEY")
		}
	})
	t.Run("2.0 from its triple", func(t *testing.T) {
		track, focus := buildTogglClients(financeconfig.Config{Toggl2Key: "k", Toggl2Organization: "1", Toggl2Workspace: "2"}, &http.Client{})
		if track != nil {
			t.Error("want no Track client without TOGGL_API_TOKEN")
		}
		if focus == nil || focus.Mode() != tracker.ModeFocus {
			t.Errorf("focus = %+v, want a Toggl 2.0 client", focus)
		}
	})
	t.Run("2.0 needs the ids", func(t *testing.T) {
		if _, focus := buildTogglClients(financeconfig.Config{Toggl2Key: "k"}, &http.Client{}); focus != nil {
			t.Error("a key without organization and workspace ids must not build a client")
		}
	})
}

func TestSelectHours(t *testing.T) {
	track := &tracker.Toggl{Token: "tok", WorkspaceID: "ws"}
	focus := tracker.NewFocus(tracker.FocusConfig{Key: "k", OrganizationID: "1", WorkspaceID: "2"}, &http.Client{})
	tests := []struct {
		name         string
		mode         string
		track, focus *tracker.Toggl
		want         tracker.Mode
	}{
		{"track", togglModeTrack, track, focus, tracker.ModeTrack},
		{"toggl2", togglModeFocus, track, focus, tracker.ModeFocus},
		{"both", togglModeBoth, track, focus, tracker.ModeBoth},
		{"disabled", "", track, focus, tracker.ModeOff},
		{"track without a client", togglModeTrack, nil, focus, tracker.ModeOff},
		{"both with one client", togglModeBoth, track, nil, tracker.ModeOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hours := selectHours(tt.mode, tt.track, tt.focus)
			if tt.want == tracker.ModeOff {
				if hours != nil {
					t.Errorf("selectHours = %v, want a true nil", hours)
				}
				return
			}
			if hours == nil || hours.Mode() != tt.want {
				t.Errorf("selectHours = %v, want %s", hours, tt.want)
			}
		})
	}
}

func TestResolveTogglMode(t *testing.T) {
	trackOnly := financeconfig.Config{TogglToken: "tok", TogglWorkspace: "ws"}
	focusOnly := financeconfig.Config{Toggl2Key: "k", Toggl2Organization: "1", Toggl2Workspace: "2"}
	both := financeconfig.Config{TogglToken: "tok", TogglWorkspace: "ws", Toggl2Key: "k", Toggl2Organization: "1", Toggl2Workspace: "2"}
	withMode := func(c financeconfig.Config, mode string) financeconfig.Config { c.TogglMode = mode; return c }

	ok := []struct {
		name string
		cfg  financeconfig.Config
		want string
	}{
		{"nothing set is disabled", financeconfig.Config{}, ""},
		{"track alone", trackOnly, togglModeTrack},
		{"2.0 alone", focusOnly, togglModeFocus},
		{"explicit track", withMode(both, togglModeTrack), togglModeTrack},
		{"explicit toggl2", withMode(both, togglModeFocus), togglModeFocus},
		{"explicit both", withMode(both, togglModeBoth), togglModeBoth},
		{"half a Track pair is ignored", financeconfig.Config{TogglToken: "tok"}, ""},
	}
	for _, tt := range ok {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTogglMode(tt.cfg)
			if err != nil || got != tt.want {
				t.Errorf("resolveTogglMode = %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	refused := map[string]financeconfig.Config{
		"both sets without a mode":  both,
		"track mode without track":  withMode(focusOnly, togglModeTrack),
		"toggl2 mode without a key": withMode(trackOnly, togglModeFocus),
		"both mode with one set":    withMode(trackOnly, togglModeBoth),
		"an unknown mode":           withMode(both, "all"),
		"a key without its ids":     financeconfig.Config{Toggl2Key: "k"},
	}
	for name, cfg := range refused {
		t.Run(name, func(t *testing.T) {
			if got, err := resolveTogglMode(cfg); err == nil {
				t.Errorf("resolveTogglMode = %q, want an error", got)
			}
		})
	}
}

func TestBuildTracker(t *testing.T) {
	t.Run("hours source is passed through", func(t *testing.T) {
		trk := buildTracker(financeconfig.Config{}, nil, &http.Client{}, "data")
		if trk.Toggl != nil {
			t.Error("want Toggl nil when no source is selected")
		}
		track := &tracker.Toggl{Token: "tok", WorkspaceID: "ws"}
		if trk := buildTracker(financeconfig.Config{}, track, &http.Client{}, "data"); trk.Toggl != tracker.HoursSource(track) {
			t.Error("want the selected source on the tracker")
		}
	})
	t.Run("budget always configured", func(t *testing.T) {
		trk := buildTracker(financeconfig.Config{}, nil, &http.Client{}, "data")
		if trk.Budget == nil {
			t.Error("want Budget always non-nil (backed by budgetDir)")
		}
	})
}
