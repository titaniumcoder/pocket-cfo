package main

import (
	"strings"
	"testing"
	"time"

	"github.com/titaniumcoder/pocket-cfo/internal/finance/tracker"
)

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		notes string
	}{
		{name: "empty is marked unset", in: "", want: unsetLabel},
		{name: "long key shows 3 leading and 2 trailing", in: "ghp_abcdefghijklmnop", want: "ghp" + strings.Repeat("*", 15) + "op"},
		{name: "exactly at the reveal threshold is fully masked", in: "0123456789", want: "**********"},
		{name: "one past the threshold reveals", in: "0123456789a", want: "012" + strings.Repeat("*", 6) + "9a"},
		{name: "short value is fully masked", in: "abc", want: "***"},
		{name: "single character is fully masked", in: "x", want: "*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskSecret(tt.in)
			if got != tt.want {
				t.Errorf("maskSecret(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len([]rune(got)) != len([]rune(tt.in)) && tt.in != "" {
				t.Errorf("maskSecret(%q) changed the length (%d -> %d), which leaks less/more than intended",
					tt.in, len([]rune(tt.in)), len([]rune(got)))
			}
		})
	}
}

// TestMaskSecretNeverRevealsMoreThanHalf is the property that matters more
// than any single case: whatever the length, a masked value must not put
// most of the real secret on screen.
func TestMaskSecretNeverRevealsMoreThanHalf(t *testing.T) {
	for _, in := range []string{"a", "ab", "abcd", "abcdefgh", "abcdefghij", "abcdefghijk", "supersecrettoken1234567890"} {
		masked := maskSecret(in)
		revealed := 0
		for i, r := range []rune(masked) {
			if r != '*' && r == []rune(in)[i] {
				revealed++
			}
		}
		if revealed*2 > len([]rune(in)) {
			t.Errorf("maskSecret(%q) = %q reveals %d of %d characters — more than half", in, masked, revealed, len([]rune(in)))
		}
	}
}

// TestMaskSecretHandlesMultibyte guards the rune-slicing: masking must not
// cut a multi-byte character in half into mojibake.
func TestMaskSecretHandlesMultibyte(t *testing.T) {
	in := "кл❤ючсекретен123"
	got := maskSecret(in)
	if !strings.HasPrefix(got, "кл❤") {
		t.Errorf("maskSecret(%q) = %q, want it to start with the first three runes", in, got)
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("maskSecret(%q) = %q contains a replacement character — a rune was sliced in half", in, got)
	}
}

// TestConfigGroupsMasksEverySecret is the regression guard that matters: no
// credential this process holds may appear in cleartext on /info, whatever
// gets added to the config struct later.
func TestConfigGroupsMasksEverySecret(t *testing.T) {
	const (
		clientSecret = "client-secret-value-9999"
		sessionKey   = "session-secret-value-8888"
		clientLink   = "client-link-secret-7777"
		otpKey       = "otp-link-secret-6666"
		togglToken   = "toggl-api-token-4444"
		toggl2Key    = "toggl_sk_key-value-3333"
		awsKey       = "aws-secret-access-key-2222"
	)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA-access-key-id-1111")
	t.Setenv("AWS_SECRET_ACCESS_KEY", awsKey)
	s := &server{cfg: config{
		env:              "prod",
		clientID:         "client-id-value-0000",
		clientSecret:     clientSecret,
		sessionSecret:    sessionKey,
		clientLinkSecret: clientLink,
		otpLinkSecret:    otpKey,
	}}
	s.cfg.finance.TogglToken = togglToken
	s.cfg.finance.Toggl2Key = toggl2Key

	var flat strings.Builder
	secretRows := 0
	for _, g := range s.configGroups() {
		for _, row := range g.Rows {
			flat.WriteString(row.Name + "=" + row.Value + "\n")
			if row.Secret {
				secretRows++
				if !strings.Contains(row.Value, "*") && row.Value != unsetLabel {
					t.Errorf("row %q is marked Secret but its value %q is unmasked", row.Name, row.Value)
				}
			}
		}
	}
	if secretRows == 0 {
		t.Fatal("no rows marked Secret — the masking guard below would pass vacuously")
	}

	rendered := flat.String()
	for _, secret := range []string{clientSecret, sessionKey, clientLink, otpKey, togglToken, toggl2Key, awsKey} {
		if strings.Contains(rendered, secret) {
			t.Errorf("cleartext secret %q appears in the /info config panel", secret)
		}
	}
}

// TestConfigGroupsNameEverySettingTheProcessReads: the table is what a
// deployment is checked against, so a variable the code reads but the page
// never names is a setting nobody can verify.
func TestConfigGroupsNameEverySettingTheProcessReads(t *testing.T) {
	t.Setenv("CONFIG_FILE", "/srv/data/config.json")
	t.Setenv("CATALOG_DIR", "/srv/catalog")
	s := &server{cfg: config{env: "prod"}}
	s.cfg.finance.HourlyRateCents = 7500
	s.cfg.finance.Currency = "EUR"

	rows := map[string]string{}
	for _, g := range s.configGroups() {
		for _, row := range g.Rows {
			rows[row.Name] = row.Value
		}
	}
	for name, want := range map[string]string{
		"CONFIG_FILE":               "/srv/data/config.json",
		"CATALOG_DIR":               "/srv/catalog",
		"DATA_UPDATED_AT":           unsetLabel,
		"DATA_COMMIT":               unsetLabel,
		"TOGGL_MODE":                "unset — disabled",
		"TOGGL_REFRESH_INTERVAL":    "15m0s",
		"TOGGL2_API_KEY_EXPIRES_AT": "unset — no advance warning, only a rejected key is reported",
		"hourlyRateCents":           "7500 (75,00 EUR an hour)",
	} {
		if got, ok := rows[name]; !ok {
			t.Errorf("no row named %s", name)
		} else if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"TOGGL_API_TOKEN", "TOGGL_WORKSPACE_ID", "togglProjectIds", "TOGGL2_API_KEY", "TOGGL2_ORGANIZATION_ID", "TOGGL2_WORKSPACE_ID", "toggl2ProjectIds", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		if _, ok := rows[name]; !ok {
			t.Errorf("no row named %s", name)
		}
	}
}

// TestTheInfoPageNamesTheDividendRates: a rate that charges a real
// distribution has to be inspectable on the running deployment, the same way
// every other dated figure in config.json already is — /info is the only place
// that says which package is actually loaded.
func TestTheInfoPageNamesTheDividendRates(t *testing.T) {
	periods, err := tracker.ParseLegislation([]tracker.LegislationEntry{
		{From: "2026-01",
			CompanyProfitTax: &tracker.TaxEntry{Bands: []tracker.BandEntry{{From: 0, Rate: ratePtr(0.10)}}},
			DividendTax:      &tracker.TaxEntry{Bands: []tracker.BandEntry{{From: 0, Rate: ratePtr(0.05)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	entries := tracker.RulesTimeline(tracker.PersonalParams{Legislation: periods}, time.Time{}, time.Now())
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one for January 2026", entries)
	}
	got := map[string]string{}
	for _, r := range entries[0].Rules {
		got[r.Name] = r.Value
	}
	for name, want := range map[string]string{"Company profit tax": "10%", "Dividend tax": "5%"} {
		if got[name] != want {
			t.Errorf("the /info rules timeline says %s = %q, want %q", name, got[name], want)
		}
	}
}

func ratePtr(v float64) *float64 { return &v }

func TestOrUnset(t *testing.T) {
	if got := orUnset(""); got != unsetLabel {
		t.Errorf("orUnset(\"\") = %q, want %q", got, unsetLabel)
	}
	if got := orUnset("value"); got != "value" {
		t.Errorf("orUnset(%q) = %q, want it unchanged", "value", got)
	}
}
