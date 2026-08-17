package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestFormatAmount covers the euro-less variant used for figures that
// aren't in euros (the api2pdf balance on /info) — same grouping and
// decimal-comma, no currency suffix.
func TestFormatAmount(t *testing.T) {
	nbsp := " "
	tests := []struct {
		name  string
		minor int64
		want  string
	}{
		{"zero", 0, "0,00"},
		{"cents only", 5, "0,05"},
		{"thousands separator", 100000, "1" + nbsp + "000,00"},
		{"thousands with cents", 102025, "1" + nbsp + "020,25"},
		{"negative", -150000, "-1" + nbsp + "500,00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatAmount(tt.minor); got != tt.want {
				t.Errorf("FormatAmount(%d) = %q, want %q", tt.minor, got, tt.want)
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

func TestFormatScaledHundredths(t *testing.T) {
	i := func(v int) *int { return &v }
	tests := []struct {
		name   string
		scaled *int
		want   string
	}{
		{"absent means one", nil, "1"},
		{"a whole number has no separator", i(300), "3"},
		{"a half", i(13650), "136,5"},
		{"two decimals", i(1025), "10,25"},
		{"a trailing zero is dropped", i(150), "1,5"},
		{"less than one", i(50), "0,5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatScaledHundredths(tt.scaled); got != tt.want {
				t.Errorf("formatScaledHundredths = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("the separator matches the money on the same page", func(t *testing.T) {
		qty := formatScaledHundredths(i(13650))
		money := FormatAmount(1020000)
		if strings.Contains(qty, ".") {
			t.Errorf("quantity %q uses a dot while money on the same page is %q", qty, money)
		}
	})
}

func TestLoadLogoSVGConfinement(t *testing.T) {
	root := t.TempDir()
	old := LogoRoot
	LogoRoot = root
	t.Cleanup(func() { LogoRoot = old })

	good := filepath.Join(root, "logo.svg")
	if err := os.WriteFile(good, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLogoSVG(good); err != nil {
		t.Errorf("a real SVG inside the root was refused: %v", err)
	}

	t.Run("a path outside the root", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "secret.svg")
		if err := os.WriteFile(outside, []byte("<svg></svg>"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadLogoSVG(outside); err == nil {
			t.Error("a logo outside the root was read")
		}
	})

	t.Run("traversal back out of the root", func(t *testing.T) {
		if _, err := loadLogoSVG(filepath.Join(root, "..", "..", "etc", "passwd.svg")); err == nil {
			t.Error("a traversal out of the root was allowed")
		}
	})

	t.Run("something that is not an svg", func(t *testing.T) {
		if _, err := loadLogoSVG(filepath.Join(root, "..", "passwd")); err == nil {
			t.Error("a non-.svg path was allowed")
		}
	})

	t.Run("an .svg whose contents are not an svg", func(t *testing.T) {
		bad := filepath.Join(root, "not-really.svg")
		if err := os.WriteFile(bad, []byte(`<script>alert(1)</script>`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadLogoSVG(bad); err == nil {
			t.Error("markup that is not an SVG was inlined into the render context")
		}
	})
}
