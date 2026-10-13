package render

import (
	"strings"
	"testing"
)

func TestRichTextPassesPlainTextThroughEscaped(t *testing.T) {
	got := string(RichText("Support & development, January"))
	want := "Support &amp; development, January"
	if got != want {
		t.Errorf("RichText = %q, want %q", got, want)
	}
}

func TestRichTextEscapesBeforeFormatting(t *testing.T) {
	got := string(RichText("**<script>alert(1)</script>**"))
	if strings.Contains(got, "<script>") {
		t.Errorf("the markup survived: %q — the text must be escaped before the markdown pass", got)
	}
	if !strings.Contains(got, "<strong>") || !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("RichText = %q, want the escaped text bolded", got)
	}
}

func TestRichTextNewlinesAreLineBreaks(t *testing.T) {
	got := string(RichText("Hosting\nDomain\nSetup fee"))
	want := "Hosting<br>Domain<br>Setup fee"
	if got != want {
		t.Errorf("RichText = %q, want %q", got, want)
	}
}

func TestRichTextBoldAndItalic(t *testing.T) {
	got := string(RichText("**Twelve hours** of *urgent* work"))
	want := "<strong>Twelve hours</strong> of <em>urgent</em> work"
	if got != want {
		t.Errorf("RichText = %q, want %q", got, want)
	}
}

func TestRichTextBullets(t *testing.T) {
	got := string(RichText("Migration:\n- export the data\n- import it twice\n- check"))
	want := "Migration:<br><ul><li>export the data</li><li>import it twice</li><li>check</li></ul>"
	if got != want {
		t.Errorf("RichText = %q, want %q", got, want)
	}
}

func TestRichTextLoneAsteriskIsLiteral(t *testing.T) {
	got := string(RichText("5 * 3 = 15"))
	want := "5 * 3 = 15"
	if got != want {
		t.Errorf("RichText = %q, want %q — a lone asterisk is not markup", got, want)
	}
}

func TestNeedsBlockLayout(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"plain single line", false},
		{"**bold** stays inline", false},
		{"*italic* too", false},
		{"two\nlines", true},
		{"- a bullet", true},
	}
	for _, tt := range tests {
		if got := NeedsBlockLayout(tt.text); got != tt.want {
			t.Errorf("NeedsBlockLayout(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
