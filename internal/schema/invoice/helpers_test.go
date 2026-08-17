package invoice

import (
	"strings"
	"testing"
)

func strp(v string) *string { return &v }

func TestLocalizedStringGet(t *testing.T) {
	ls := LocalizedString{De: strp("Hallo"), Bg: strp("Здравей")}

	if got, ok := ls.Get(InvoiceJsonLanguageDe); !ok || got != "Hallo" {
		t.Errorf("Get(de) = %q, %v, want %q, true", got, ok, "Hallo")
	}
	if got, ok := ls.Get(InvoiceJsonLanguageBg); !ok || got != "Здравей" {
		t.Errorf("Get(bg) = %q, %v, want %q, true", got, ok, "Здравей")
	}
	if _, ok := ls.Get(InvoiceJsonLanguageEn); ok {
		t.Error("Get(en) = true, want false (not set)")
	}
	if _, ok := ls.Get(InvoiceJsonLanguageFr); ok {
		t.Error("Get(fr) = true, want false (not set)")
	}
}

func TestLocalizedStringIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		ls   LocalizedString
		want bool
	}{
		{"nothing set", LocalizedString{}, true},
		{"only de set", LocalizedString{De: strp("Hallo")}, false},
		{"only bg set", LocalizedString{Bg: strp("Здравей")}, false},
		{"fully populated", LocalizedString{De: strp("Hallo"), En: strp("Hello"), Fr: strp("Bonjour"), Bg: strp("Здравей")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ls.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocalizedStringRequire(t *testing.T) {
	t.Run("bg-language invoice needs only bg", func(t *testing.T) {
		ls := LocalizedString{Bg: strp("Здравей")}
		primary, secondary, err := ls.Require(InvoiceJsonLanguageBg)
		if err != nil {
			t.Fatalf("Require(bg): %v", err)
		}
		if primary != "Здравей" || secondary != "" {
			t.Errorf("Require(bg) = %q, %q, want %q, \"\"", primary, secondary, "Здравей")
		}
	})

	t.Run("non-bg invoice needs its own language plus bg", func(t *testing.T) {
		ls := LocalizedString{De: strp("Hallo"), Bg: strp("Здравей")}
		primary, secondary, err := ls.Require(InvoiceJsonLanguageDe)
		if err != nil {
			t.Fatalf("Require(de): %v", err)
		}
		if primary != "Hallo" || secondary != "Здравей" {
			t.Errorf("Require(de) = %q, %q, want %q, %q", primary, secondary, "Hallo", "Здравей")
		}
	})

	t.Run("missing own-language key fails loudly", func(t *testing.T) {
		ls := LocalizedString{Bg: strp("Здравей")}
		if _, _, err := ls.Require(InvoiceJsonLanguageEn); err == nil {
			t.Fatal("Require(en) with no en key: expected an error, got nil")
		}
	})

	t.Run("missing bg fails loudly for a non-bg invoice", func(t *testing.T) {
		ls := LocalizedString{De: strp("Hallo")}
		if _, _, err := ls.Require(InvoiceJsonLanguageDe); err == nil {
			t.Fatal("Require(de) with no bg key: expected an error, got nil")
		}
	})
}

func TestRenderedTexts(t *testing.T) {
	both := LocalizedString{De: strp("Hinweis"), Bg: strp("Бележка")}

	tests := []struct {
		name string
		ls   LocalizedString
		lang InvoiceJsonLanguage
		want []string
	}{
		{"a bg invoice prints one column", both, InvoiceJsonLanguageBg, []string{"Бележка"}},
		{"a de invoice prints its own text and the bg beside it", both, InvoiceJsonLanguageDe, []string{"Hinweis", "Бележка"}},
		{"a language with no text falls back to the bg alone", both, InvoiceJsonLanguageFr, []string{"Бележка"}},
		{"no bg sibling prints the primary alone", LocalizedString{De: strp("Hinweis")}, InvoiceJsonLanguageDe, []string{"Hinweis"}},
		{"nothing at all prints nothing", LocalizedString{}, InvoiceJsonLanguageDe, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ls.RenderedTexts(tt.lang)
			if len(got) != len(tt.want) {
				t.Fatalf("RenderedTexts = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("RenderedTexts[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestValidateLocalizationReportsEveryGap(t *testing.T) {
	inv := &InvoiceJson{
		Language: InvoiceJsonLanguageDe,
		Lines: []Line{
			{Description: LocalizedString{De: strp("Arbeit")}},
			{Description: LocalizedString{Bg: strp("Работа")}},
		},
		Discounts: []Discount{{Label: LocalizedString{De: strp("Rabatt")}}},
		Tax:       Tax{Note: LocalizedString{De: strp("Hinweis")}},
	}
	err := ValidateLocalization(inv)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"line 1", "line 2", "discount 1", "tax note"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to also report %q", err, want)
		}
	}
}
