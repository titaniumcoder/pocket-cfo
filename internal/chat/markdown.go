package chat

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

var (
	boldRE   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	codeRE   = regexp.MustCompile("`([^`]+)`")
	bulletRE = regexp.MustCompile(`^\s*[-*•]\s+`)
	numberRE = regexp.MustCompile(`^\s*\d+[.)]\s+`)
)

func Markdown(text string) template.HTML {
	var out strings.Builder
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var paragraph []string
	var list []string
	listTag := ""

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		out.WriteString("<p>" + strings.Join(paragraph, "<br>") + "</p>")
		paragraph = nil
	}
	flushList := func() {
		if len(list) == 0 {
			return
		}
		out.WriteString("<" + listTag + "><li>" + strings.Join(list, "</li><li>") + "</li></" + listTag + ">")
		list = nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			flushParagraph()
			flushList()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			out.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>")
			continue
		}
		if strings.TrimSpace(line) == "" {
			flushParagraph()
			flushList()
			continue
		}
		if h := strings.TrimLeft(line, "#"); h != line && strings.HasPrefix(h, " ") {
			flushParagraph()
			flushList()
			out.WriteString("<p><strong>" + inline(strings.TrimSpace(h)) + "</strong></p>")
			continue
		}
		if bulletRE.MatchString(line) || numberRE.MatchString(line) {
			tag := "ul"
			item := bulletRE.ReplaceAllString(line, "")
			if numberRE.MatchString(line) {
				tag = "ol"
				item = numberRE.ReplaceAllString(line, "")
			}
			flushParagraph()
			if listTag != tag {
				flushList()
				listTag = tag
			}
			list = append(list, inline(item))
			continue
		}
		flushList()
		paragraph = append(paragraph, inline(line))
	}
	flushParagraph()
	flushList()
	return template.HTML(out.String())
}

func inline(s string) string {
	escaped := html.EscapeString(s)
	escaped = codeRE.ReplaceAllString(escaped, "<code>$1</code>")
	escaped = boldRE.ReplaceAllString(escaped, "<strong>$1</strong>")
	return escaped
}
