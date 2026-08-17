package render

import (
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

func strp(v string) *string { return &v }

func TestBilingual(t *testing.T) {
	t.Run("bg language has no secondary", func(t *testing.T) {
		ls := invoice.LocalizedString{Bg: strp("Здравей")}
		got := bilingual(ls, invoice.InvoiceJsonLanguageBg)
		want := Bilingual{Primary: "Здравей"}
		if got != want {
			t.Errorf("bilingual(bg) = %+v, want %+v", got, want)
		}
	})

	t.Run("short pair fits inline", func(t *testing.T) {
		ls := invoice.LocalizedString{De: strp("Hallo"), Bg: strp("Здравей")}
		got := bilingual(ls, invoice.InvoiceJsonLanguageDe)
		if !got.Inline {
			t.Errorf("bilingual(short pair) = %+v, want Inline=true", got)
		}
		if got.Primary != "Hallo" || got.Secondary != "Здравей" {
			t.Errorf("bilingual(short pair) = %+v, want Primary=Hallo Secondary=Здравей", got)
		}
	})

	t.Run("long pair stacks", func(t *testing.T) {
		long := strings.Repeat("x", 40)
		ls := invoice.LocalizedString{De: strp(long), Bg: strp(long)}
		got := bilingual(ls, invoice.InvoiceJsonLanguageDe)
		if got.Inline {
			t.Errorf("bilingual(long pair) = %+v, want Inline=false", got)
		}
	})

	t.Run("the threshold counts characters, not bytes", func(t *testing.T) {
		latin := bilingual(invoice.LocalizedString{
			De: strp(strings.Repeat("a", 25)),
			Bg: strp(strings.Repeat("b", 25)),
		}, invoice.InvoiceJsonLanguageDe)
		cyrillic := bilingual(invoice.LocalizedString{
			De: strp(strings.Repeat("a", 25)),
			Bg: strp(strings.Repeat("б", 25)),
		}, invoice.InvoiceJsonLanguageDe)

		if !latin.Inline {
			t.Fatalf("a 50-character latin pair = %+v, want Inline=true", latin)
		}
		if !cyrillic.Inline {
			t.Errorf("a 50-character pair with a Cyrillic half = %+v, want Inline=true — it is the same length on the page", cyrillic)
		}
	})

	t.Run("missing bg translation renders empty secondary, no crash", func(t *testing.T) {
		ls := invoice.LocalizedString{De: strp("Hallo")}
		got := bilingual(ls, invoice.InvoiceJsonLanguageDe)
		want := Bilingual{Primary: "Hallo"}
		if got != want {
			t.Errorf("bilingual(missing bg) = %+v, want %+v", got, want)
		}
	})
}
