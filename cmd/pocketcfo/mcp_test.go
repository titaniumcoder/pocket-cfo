package main

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These drive /mcp over raw HTTP with hand-written JSON-RPC rather than
// through the SDK's own client, so they would equally validate a hand-rolled
// replacement — which is what keeps the dependency reversible.

func mcpCall(t *testing.T, s *server, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

const initReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

// decodeRPC handles both a plain JSON body and an SSE-framed one, so the test
// doesn't depend on which the server chose.
func decodeRPC(t *testing.T, body string) map[string]any {
	t.Helper()
	payload := strings.TrimSpace(body)
	if strings.Contains(payload, "data:") {
		for _, line := range strings.Split(payload, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
				payload = strings.TrimSpace(rest)
				break
			}
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("response is not JSON-RPC: %v\n%s", err, body)
	}
	return out
}

// TestMCPUnauthenticatedLearnsNothing: the gate sits in front of the protocol,
// so even initialize and tools/list are refused and a caller learns nothing
// about the tool surface — not even that it exists.
func TestMCPUnauthenticatedLearnsNothing(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	for _, body := range []string{
		initReq,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_budget_categories","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"server/discover"}`,
	} {
		w := mcpCall(t, s, "", body)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 for %.40s", w.Code, body)
		}
		for _, leak := range []string{"list_budget_categories", "put_actuals", "Rent", "tools"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("a 401 body leaked %q: %s", leak, w.Body)
			}
		}
	}

	// A bare GET is refused before the transport can answer it.
	r := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /mcp = %d, want 401", w.Code)
	}
}

func TestMCPAbsentWithoutToken(t *testing.T) {
	s := apiServer(t, "", "")
	w := mcpCall(t, s, "", initReq)
	if w.Code == http.StatusOK {
		t.Errorf("/mcp answered 200 with no token configured: %s", w.Body)
	}
}

func TestMCPToolsList(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	w := mcpCall(t, s, apiTestToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	resp := decodeRPC(t, w.Body.String())
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("no tools in %v", result)
	}

	want := map[string]bool{
		"list_budget_categories": true, "get_budget": true, "get_actuals": true,
		"search_transactions": true, "get_reconciliation_status": true,
		"list_accounts": true, "put_actuals": true, "move_planned_expense": true,
	}
	got := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name, _ := tool["name"].(string)
		got[name] = true
		if desc, _ := tool["description"].(string); desc == "" {
			t.Errorf("%s has no description — the contract lives there", name)
		}
		if tool["inputSchema"] == nil {
			t.Errorf("%s has no input schema", name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("tool %q missing; got %v", name, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d tools, want %d: %v", len(got), len(want), got)
	}
}

// TestMCPPutActualsDescribesItsContract: the description is where Hermes
// learns that omitting a transaction is a removal, so pin it.
func TestMCPPutActualsDescribesItsContract(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	w := mcpCall(t, s, apiTestToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp := decodeRPC(t, w.Body.String())
	tools := resp["result"].(map[string]any)["tools"].([]any)

	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] != "put_actuals" {
			continue
		}
		desc := strings.ToLower(tool["description"].(string))
		for _, want := range []string{"whole-month", "removal", "re-read"} {
			if !strings.Contains(desc, want) {
				t.Errorf("put_actuals description does not mention %q: %s", want, desc)
			}
		}
		return
	}
	t.Fatal("put_actuals not listed")
}

func TestMCPToolsCall(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_budget_categories","arguments":{}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Rent") || !strings.Contains(body, "Accounting") {
		t.Errorf("tools/call did not return the categories: %s", body)
	}
}

// TestMCPAndRESTAgree: the adapters must not drift, so the same question asked
// both ways must give the same figures.
func TestMCPAndRESTAgree(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	rest := apiGet(t, s, "/api/budget/2026-08", apiTestToken)
	var restBody struct {
		TotalPrivateCents int `json:"total_private_cents"`
		TotalCompanyCents int `json:"total_company_cents"`
	}
	if err := json.Unmarshal(rest.Body.Bytes(), &restBody); err != nil {
		t.Fatal(err)
	}

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_budget","arguments":{"period":"2026-08"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	mcpBody := w.Body.String()
	for _, want := range []string{
		`"total_private_cents":90000`,
		`"total_company_cents":15000`,
	} {
		if !strings.Contains(strings.ReplaceAll(mcpBody, " ", ""), want) {
			t.Errorf("MCP result does not contain %s: %s", want, mcpBody)
		}
	}
	if restBody.TotalPrivateCents != 90000 || restBody.TotalCompanyCents != 15000 {
		t.Errorf("REST disagrees: %+v", restBody)
	}
}

// TestMCPServiceErrorsSurfaceTheirCode: a refusal must carry the same code the
// REST adapter would map, so Hermes can react identically either way.
func TestMCPServiceErrorsSurfaceTheirCode(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_actuals","arguments":{"month":"2026-07"}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not_found") {
		t.Errorf("the tool error does not carry its code: %s", w.Body)
	}
}

func TestMCPUnknownToolIsAnErrorNotAPanic(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`)
	if w.Code >= 500 {
		t.Errorf("status = %d, want a JSON-RPC error rather than a server error", w.Code)
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "error") {
		t.Errorf("body = %s, want an error", w.Body)
	}
}

// TestMCPSDKIsImportedOnce keeps the dependency reversible: it is confined to
// internal/api/mcp.go, so deleting that file leaves REST and the service
// untouched.
func TestMCPSDKIsImportedOnce(t *testing.T) {
	const sdk = "github.com/modelcontextprotocol/go-sdk"
	var importers []string

	err := filepath.Walk("../..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil
		}
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, sdk) {
				importers = append(importers, filepath.ToSlash(path))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(importers) != 1 || !strings.HasSuffix(importers[0], "internal/api/mcp.go") {
		t.Errorf("the SDK is imported by %v; it belongs only in internal/api/mcp.go, so deleting that file drops the dependency", importers)
	}
}

// TestMCPAndRESTAgreeOnSearch is the test whose absence let the two adapters
// drift: deriving the years a from/to range covers lived in the REST adapter
// only, so the identical search through MCP scanned the current year and
// returned nothing — no error, no warning, just an empty answer that reads as
// "never seen this merchant". Comparing the two answers is the cheap way to
// notice; comparing each against a hand-written expectation is not, because
// whoever writes the second expectation writes it to match the code.
func TestMCPAndRESTAgreeOnSearch(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	// A year that is NOT the current one: with 2026-08 the current-year
	// fallback finds the match by accident and the test passes against the
	// very bug it exists for. This month is only reachable by deriving the
	// year from the range.
	const from, to = "2025-11", "2025-11"

	rest := apiGet(t, s, "/api/transactions?q=LIDL&from="+from+"&to="+to+"&include_ignored=true", apiTestToken)
	if rest.Code != http.StatusOK {
		t.Fatalf("REST status = %d, body %s", rest.Code, rest.Body)
	}
	var restBody struct {
		Transactions []struct {
			ID    string `json:"id"`
			Month string `json:"month"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(rest.Body.Bytes(), &restBody); err != nil {
		t.Fatal(err)
	}
	if len(restBody.Transactions) == 0 {
		t.Fatal("the fixture matched nothing over REST; the test proves nothing")
	}

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_transactions","arguments":{"query":"LIDL","from":"`+from+`","to":"`+to+`","include_ignored":true}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("MCP status = %d, body %s", w.Code, w.Body)
	}
	mcpBody := w.Body.String()
	for _, tx := range restBody.Transactions {
		if !strings.Contains(mcpBody, `"`+tx.ID+`"`) {
			t.Errorf("REST found %s in %s and MCP did not: %s", tx.ID, tx.Month, mcpBody)
		}
	}
}

// TestMCPRejectsAnImpossibleYear: the range check belongs to the service, so
// it applies whoever is asking. It used to live in the REST handler, which
// meant MCP accepted year 99 and answered with twelve empty months.
func TestMCPRejectsAnImpossibleYear(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_reconciliation_status","arguments":{"year":99}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_request") {
		t.Errorf("year 99 was accepted: %s", w.Body)
	}
}

// TestMCPBodyIsCapped: put_actuals is the one tool that accepts a document, so
// an uncapped body here is an uncapped body full stop.
func TestMCPBodyIsCapped(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	huge := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_transactions","arguments":{"query":"` +
		strings.Repeat("x", maxMCPBody+1) + `"}}}`
	w := mcpCall(t, s, apiTestToken, huge)
	if w.Code == http.StatusOK {
		t.Errorf("a %d-byte body was accepted", len(huge))
	}
}
