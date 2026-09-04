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
	}
	if len(seen) != 19 {
		t.Errorf("catalog lists %d tools, want 19", len(seen))
	}
}

func TestToolSchemaCarriesTheFieldDescriptions(t *testing.T) {
	s := &Service{}
	for _, tool := range s.Tools() {
		if tool.Name != "add_transactions" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"transactions"`, `"coverage"`, "POSITIVE = money out"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("add_transactions schema lacks %s:\n%s", want, raw)
			}
		}
		return
	}
	t.Fatal("add_transactions is not in the catalog")
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
