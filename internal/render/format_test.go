package render

import "testing"

// TestFormatMoney pins the exact nbsp-grouped Bulgarian/European format —
// "looks right" and "is right" can silently diverge on an invisible
// character like U+00A0, so assert the literal bytes.
func TestFormatMoney(t *testing.T) {
	nbsp := " "
	tests := []struct {
		name  string
		minor int64
		want  string
	}{
		{"zero", 0, "0,00 €"},
		{"cents only", 5, "0,05 €"},
		{"one euro", 100, "1,00 €"},
		{"just under a thousand", 99999, "999,99 €"},
		{"thousands separator", 100000, "1" + nbsp + "000,00 €"},
		{"thousands with cents", 102025, "1" + nbsp + "020,25 €"},
		{"millions", 102000000, "1" + nbsp + "020" + nbsp + "000,00 €"},
		{"negative", -150000, "-1" + nbsp + "500,00 €"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatMoney(tt.minor); got != tt.want {
				t.Errorf("FormatMoney(%d) = %q, want %q", tt.minor, got, tt.want)
			}
		})
	}
}

func TestCountryName(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		{"Austria", "AT", "Austria"},
		{"Switzerland", "CH", "Switzerland"},
		{"Bulgaria", "BG", "Bulgaria"},
		{"unknown code falls back to itself", "ZZ", "ZZ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CountryName(tt.code); got != tt.want {
				t.Errorf("CountryName(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}
