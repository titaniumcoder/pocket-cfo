package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestToolCatalogIsWellFormed(t *testing.T) {
	s := &Service{}
	seen := map[string]bool{}
	for _, tool := range s.Tools() {
		if seen[tool.Name] {
			t.Errorf("tool %q is listed twice", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil || tool.InputSchema.Type != "object" {
			t.Errorf("tool %q: input schema must be an object", tool.Name)
		}
		if tool.Call == nil {
			t.Errorf("tool %q cannot be called", tool.Name)
		}
		if !tool.ReadOnly {
			t.Errorf("tool %q is not read-only — the surface is reads only", tool.Name)
		}
	}
	want := map[string]bool{
		"list_budget_categories": true, "get_budget": true, "get_actuals": true,
		"search_transactions": true, "list_accounts": true, "list_invoices": true,
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("tool %q is missing", name)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("catalog lists %d tools, want %d", len(seen), len(want))
	}
}

func TestToolRefusesAnUnknownArgument(t *testing.T) {
	s := &Service{}
	for _, tool := range s.Tools() {
		if tool.Name != "get_actuals" {
			continue
		}
		_, err := tool.Call(context.Background(), json.RawMessage(`{"month":"2026-08","typo":1}`))
		e, ok := err.(*Error)
		if !ok || e.Code != CodeInvalidRequest || !strings.Contains(e.Message, "typo") {
			t.Fatalf("want an invalid_request naming the field, got %v", err)
		}
		return
	}
	t.Fatal("get_actuals is not in the catalog")
}
