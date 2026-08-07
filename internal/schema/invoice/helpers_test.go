package invoice

import "testing"

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
