package main

import (
	"strings"
	"testing"

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
		api2pdfKey   = "api2pdf-key-value-5555"
		togglToken   = "toggl-api-token-4444"
	)
	s := &server{cfg: config{
		env:              "prod",
		clientID:         "client-id-value-0000",
		clientSecret:     clientSecret,
		sessionSecret:    sessionKey,
		clientLinkSecret: clientLink,
		otpLinkSecret:    otpKey,
		api2pdfKey:       api2pdfKey,
	}}
	s.cfg.finance.TogglToken = togglToken

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
	for _, secret := range []string{clientSecret, sessionKey, clientLink, otpKey, api2pdfKey, togglToken} {
		if strings.Contains(rendered, secret) {
			t.Errorf("cleartext secret %q appears in the /info config panel", secret)
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

	summary := legislationSummary(periods)
	for _, want := range []string{"company profit tax 10%", "dividend tax 5%"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the /info legislation summary %q never says %q", summary, want)
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
