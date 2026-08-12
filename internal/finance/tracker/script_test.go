package tracker

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// scriptsIn returns the contents of every <script> element in a page.
func scriptsIn(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// balanced reports the first unclosed or over-closed bracket in src, ignoring
// brackets inside strings and comments. It is not a parser, but it catches the
// one mistake that actually happens when a script lives inside a Go string
// literal: an edit that drops a closing brace. That silently kills the whole
// script — the browser parses nothing, no listener is registered, and the
// feature simply does not respond, which is exactly how the copy link shipped
// broken.
func balanced(src string) string {
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	var stack []rune
	var quote rune
	var line = 1
	runes := []rune(src)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\n' {
			line++
		}
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"' || c == '`':
			quote = c
			continue
		case c == '/' && i+1 < len(runes) && runes[i+1] == '/':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			line++
			continue
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			for i+1 < len(runes) && !(runes[i] == '*' && runes[i+1] == '/') {
				if runes[i] == '\n' {
					line++
				}
				i++
			}
			i++
			continue
		}
		switch c {
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			if len(stack) == 0 {
				return "closing " + string(c) + " with nothing open, line " + itoa(line)
			}
			if stack[len(stack)-1] != pairs[c] {
				return "closing " + string(c) + " does not match " + string(stack[len(stack)-1]) + ", line " + itoa(line)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		return "unclosed " + string(stack[len(stack)-1]) + " at end of script"
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestRenderedScriptsParse guards every inline script the app serves. These
// live inside Go string literals, so nothing about editing them tells you when
// one stops being valid JavaScript — the compiler is happy, the tests that
// grep for a substring are happy, and the page silently loses the feature.
func TestRenderedScriptsParse(t *testing.T) {
	trk := actualsTracker(t, map[string]string{
		"actuals/2026-08.json": `{"month":"2026-08","coverage":[{"account":"A","from":"2026-08-01","to":"2026-08-31","imported_at":"2026-09-01"}],
			"transactions":[{"id":"t1","date":"2026-08-03","description":"LIDL","amount":210.4,"account":"A","category":"00000000-0000-4000-8000-000000000001"},
			                {"id":"t2","date":"2026-08-04","description":"TRANSFER","amount":-500,"account":"A","ignored":"own account"}]}`,
	})
	ctx := context.Background()

	pages := map[string]string{}
	rec := httptest.NewRecorder()
	RenderSpending(rec, trk.ComputeSpending(ctx, 2026, time.August))
	pages["spending"] = rec.Body.String()
	rec = httptest.NewRecorder()
	RenderPage(rec, trk.ComputeMonth(ctx, 2026, time.August))
	pages["dashboard"] = rec.Body.String()

	node, nodeErr := exec.LookPath("node")
	for name, body := range pages {
		scripts := scriptsIn(body)
		if len(scripts) == 0 {
			t.Errorf("%s has no inline script; this test would pass vacuously", name)
		}
		for i, src := range scripts {
			if problem := balanced(src); problem != "" {
				t.Errorf("%s script %d is not valid JavaScript: %s", name, i, problem)
			}
			if nodeErr != nil {
				continue // the balance check above is the one CI relies on
			}
			cmd := exec.Command(node, "--check", "-")
			cmd.Stdin = strings.NewReader(src)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s script %d fails node --check: %v\n%s", name, i, err, out)
			}
		}
	}
}

// TestBalancedCatchesADroppedBrace pins the guard itself: a checker that never
// fails is worse than no checker, because it reads as coverage.
func TestBalancedCatchesADroppedBrace(t *testing.T) {
	good := "document.addEventListener('click', function (e) {\n  var s = '} not a brace';\n  // } neither\n  if (e) { return; }\n});"
	if problem := balanced(good); problem != "" {
		t.Errorf("valid script rejected: %s", problem)
	}
	// The exact shape that shipped: the handler's own closing }); dropped.
	bad := "document.addEventListener('click', function (e) {\n  function inner() {\n  }\n"
	if balanced(bad) == "" {
		t.Error("an unclosed handler was accepted; that is how the copy link shipped dead")
	}
	if balanced("f())") == "" {
		t.Error("a stray closing paren was accepted")
	}
}
