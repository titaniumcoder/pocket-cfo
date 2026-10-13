package render

import (
	"html"
	"regexp"
	"strings"

	htmltemplate "html/template"
)

var (
	boldPattern   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	italicPattern = regexp.MustCompile(`\*([^*\n]+)\*`)
)

// RichText renders the small formatting subset invoice line descriptions
// support: a newline is a line break, `**bold**`, `*italic*`, and lines
// starting with `- ` become a bullet list. The text is escaped FIRST, so the
// markdown pass works on escaped text and cannot produce anything but these
// four shapes — a `<` in a description stays a literal `<` on the page.
func RichText(s string) htmltemplate.HTML {
	esc := html.EscapeString(s)
	lines := strings.Split(esc, "\n")

	var out strings.Builder
	var bullets []string
	flush := func() {
		if len(bullets) == 0 {
			return
		}
		out.WriteString("<ul>")
		for _, b := range bullets {
			out.WriteString("<li>" + b + "</li>")
		}
		out.WriteString("</ul>")
		bullets = nil
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			if i > 0 && len(bullets) == 0 {
				out.WriteString("<br>")
			}
			bullets = append(bullets, inlineMarkdown(trimmed[2:]))
			continue
		}
		flush()
		if i > 0 {
			out.WriteString("<br>")
		}
		out.WriteString(inlineMarkdown(trimmed))
	}
	flush()
	return htmltemplate.HTML(out.String())
}

func inlineMarkdown(escaped string) string {
	escaped = boldPattern.ReplaceAllString(escaped, "<strong>$1</strong>")
	return italicPattern.ReplaceAllString(escaped, "<em>$1</em>")
}

// NeedsBlockLayout says whether a description renders as more than one inline
// run — a forced line break or a bullet list — and so cannot sit in the
// single-line layout the short bilingual descriptions use.
func NeedsBlockLayout(s string) bool {
	if strings.Contains(s, "\n") {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			return true
		}
	}
	return false
}
