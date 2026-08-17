package tracker

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/webui"
)

// The Content-Security-Policy in cmd/pocketcfo/main.go sets no script-src, so
// scripts fall back to default-src 'self'. Under that policy a browser refuses
// an inline <script> and an inline on*= handler, and says so only in its
// console — the page still renders, the behaviour is simply gone. That is how
// the category accordion and both period pickers were dead from v0.26.0 to
// v0.27.2 without anything failing.
//
// So the templates are held to the policy here rather than in a browser: any
// script has to come from a src the policy allows, and handlers have to be
// bound in static/app.js. Relaxing the CSP instead would turn this test into
// the thing that has to change, which is the point of writing it this way.

var (
	inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	scriptTag     = regexp.MustCompile(`(?i)<script(\s[^>]*)?>`)
)

func checkNoInlineJS(t *testing.T, name, html string) {
	t.Helper()

	for _, m := range inlineHandler.FindAllStringIndex(html, -1) {
		t.Errorf("%s: inline event handler %q at byte %d — the CSP blocks it; bind it in static/app.js instead",
			name, strings.TrimSpace(html[m[0]:m[1]]), m[0])
	}

	for _, m := range scriptTag.FindAllStringSubmatchIndex(html, -1) {
		tag := html[m[0]:m[1]]
		if !strings.Contains(strings.ToLower(tag), "src=") {
			t.Errorf("%s: inline <script> at byte %d — the CSP blocks it; move the code to static/app.js",
				name, m[0])
		}
	}
}

func TestTemplatesCarryNoInlineJavaScript(t *testing.T) {
	checkNoInlineJS(t, "tracker templates", templates)
	checkNoInlineJS(t, "webui.HeaderTemplate", webui.HeaderTemplate)

	dir := filepath.Join("..", "..", "..", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	var seen int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		seen++
		checkNoInlineJS(t, e.Name(), string(b))
	}
	if seen == 0 {
		t.Fatalf("no .html templates found in %s — this test would pass by looking at nothing", dir)
	}
}

// The behaviours the inline code used to provide still have to exist somewhere,
// or the test above would be satisfied by deleting them.
func TestStaticAppJSBindsWhatTheTemplatesStoppedDoingInline(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "static", "app.js"))
	if err != nil {
		t.Fatalf("read static/app.js: %v", err)
	}
	js := string(b)

	for _, want := range []string{
		".group-header", // the accordion
		"select[data-nav]",
		"'spending'",
		"'month'",
		"'year'",
		".copy-tx",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("static/app.js does not mention %s, so whatever used to handle it is now unbound", want)
		}
	}
}
