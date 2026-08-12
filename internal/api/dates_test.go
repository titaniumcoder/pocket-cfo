package api

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dayFirst matches a rendered dd.mm.yyyy. The screen uses that format; this
// package must not, whoever asks.
var dayFirst = regexp.MustCompile(`\b\d{2}\.\d{2}\.\d{4}\b`)

// iso matches a yyyy-mm or yyyy-mm-dd, so each subtest can prove its payload
// carries a date at all before claiming that date is in the right format.
var iso = regexp.MustCompile(`\b\d{4}-\d{2}(-\d{2})?\b`)

// TestAPIDatesAreISO: the frontend reads dd.mm.yyyy and the API always speaks
// yyyy-mm-dd. They are different audiences — a human reading a screen knows
// the local convention, and a model reading JSON does not, so 01.08.2026 over
// the wire is a date that means two things.
//
// This walks every read the service offers rather than checking one, since
// the risk is a single endpoint being helpful.
func TestAPIDatesAreISO(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	reads := map[string]func() (any, error){
		"budget/categories": func() (any, error) { return s.Categories(ctx) },
		"budget/2026-08":    func() (any, error) { return s.BudgetForMonth(ctx, "2026-08") },
		"accounts":          func() (any, error) { return s.AccountsList(ctx) },
		"actuals/2026-08":   func() (any, error) { return s.ActualsFor(ctx, "2026-08") },
		"transactions":      func() (any, error) { return s.Search(ctx, SearchQuery{Years: []int{2026}, IncludeIgnored: true}) },
		"reconciliation":    func() (any, error) { return s.Reconciliation(ctx, 2026) },
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if m := dayFirst.FindString(string(body)); m != "" {
				t.Errorf("%s emitted %s — the API is yyyy-mm-dd, always", name, m)
			}
			if !iso.Match(body) {
				t.Errorf("%s carries no date at all; this subtest proves nothing", name)
			}
		})
	}
}

// TestAPINeverFormatsADate is the structural half: the display formatter lives
// in internal/finance/tracker and nothing here may reach for it, or for the
// layout it uses. A test on output alone would pass until the one endpoint
// that formats happens to be exercised.
func TestAPINeverFormatsADate(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// Parsed rather than grepped, so a comment mentioning the format is
		// not mistaken for code producing it.
		f, err := parser.ParseFile(fset, e.Name(), src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.Contains(lit.Value, "02.01.2006") || strings.Contains(lit.Value, "2 January 2006") {
				t.Errorf("%s uses a display date layout at %s; the API is yyyy-mm-dd", e.Name(), fset.Position(lit.Pos()))
			}
			return true
		})
	}
}
