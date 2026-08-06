package render

import "testing"

// TestFormatMoney pins the exact nbsp-grouped Bulgarian/European format —
// "looks right" and "is right" can silently diverge on an invisible
// character like U+00A0, so assert the literal bytes.
func TestFormatMoney(t *testing.T) {
	nbsp := " "
	cases := []struct {
		minor int64
		want  string
	}{
		{0, "0,00 €"},
		{5, "0,05 €"},
		{100, "1,00 €"},
		{99999, "999,99 €"},
		{100000, "1" + nbsp + "000,00 €"},
		{102025, "1" + nbsp + "020,25 €"},
		{102000000, "1" + nbsp + "020" + nbsp + "000,00 €"},
		{-150000, "-1" + nbsp + "500,00 €"},
	}
	for _, c := range cases {
		if got := FormatMoney(c.minor); got != c.want {
			t.Errorf("FormatMoney(%d) = %q, want %q", c.minor, got, c.want)
		}
	}
}

func TestCountryName(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"AT", "Austria"},
		{"CH", "Switzerland"},
		{"BG", "Bulgaria"},
		{"ZZ", "ZZ"}, // unknown code falls back to itself
	}
	for _, c := range cases {
		if got := CountryName(c.code); got != c.want {
			t.Errorf("CountryName(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}
