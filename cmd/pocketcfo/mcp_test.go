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
		for _, leak := range []string{"list_budget_categories", "add_transactions", "Rent", "tools"} {
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
		"list_accounts": true, "add_transactions": true, "edit_transactions": true,
		"move_planned_expense": true,
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

// TestMCPWriteToolsDescribeTheirContract: the description is the only thing
// Hermes reads at call time — HERMES.md may not be in context and the input
// schema says nothing about intent. Every rule the Go code enforces has to be
// findable here, so pin the ones that decide whether a write is safe.
func TestMCPWriteToolsDescribeTheirContract(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	w := mcpCall(t, s, apiTestToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp := decodeRPC(t, w.Body.String())
	tools := resp["result"].(map[string]any)["tools"].([]any)

	want := map[string][]string{
		// That it never removes, that the month is derived rather than sent,
		// that a re-send is safe, and what to do with money you cannot place.
		"add_transactions": {"never touches", "removes", "date decides", "skipped", "untracked"},
		// That a disposition is replaced wholesale, that the statement facts
		// and other lines are off limits, and that there is no lock to pass.
		"edit_transactions": {"replaces", "cannot", "left alone", "no base_sha", "month"},
	}
	desc := map[string]string{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name, _ := tool["name"].(string)
		desc[name] = strings.ToLower(tool["description"].(string))
	}
	for name, phrases := range want {
		got, ok := desc[name]
		if !ok {
			t.Errorf("%s is not listed", name)
			continue
		}
		for _, phrase := range phrases {
			if !strings.Contains(got, phrase) {
				t.Errorf("%s description does not mention %q:\n%s", name, phrase, got)
			}
		}
	}
	// And the retired tool must be gone, not merely undocumented: its whole
	// contract was that omitting a line deletes it.
	if _, ok := desc["put_actuals"]; ok {
		t.Error("put_actuals is still listed")
	}
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

// TestMCPBodyIsCapped: add_transactions accepts a whole statement import, so
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

// TestMCPAndRESTAgreeOnCoverageOnlyAdd: reporting that you read further into a
// month and found nothing new is a real import — it moves the completeness the
// dashboard withholds judgement on. Both surfaces must accept it and give the
// same answer, and the MCP one must not refuse it in the SDK's schema layer
// before the service is ever reached.
func TestMCPAndRESTAgreeOnCoverageOnlyAdd(t *testing.T) {
	body := `{"coverage":[{"account":"A","from":"2026-08-01","to":"2026-09-15","imported_at":"2026-09-16"}]}`

	s := putServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/actuals/add", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+apiTestToken)
	r.Header.Set("Content-Type", "application/json")
	rest := httptest.NewRecorder()
	s.routes().ServeHTTP(rest, r)

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add_transactions","arguments":`+body+`}}`)
	mcp := w.Body.String()

	// The range crosses a month boundary, so both must refuse it — and for the
	// same reason, in our words rather than the schema validator's.
	const reason = "crosses a month boundary"
	if !strings.Contains(rest.Body.String(), reason) {
		t.Errorf("REST said %s, want the boundary refusal", rest.Body)
	}
	if !strings.Contains(mcp, reason) {
		t.Errorf("MCP said %s, want the boundary refusal — a required-property error means the schema refused it first", mcp)
	}
}

// TestMCPResultsAreObjects: MCP requires a tool's structured content to be a
// JSON object. A service method returning a slice puts an array at the top
// level, which a strict client rejects outright — the whole tool becomes
// unusable, which is how list_budget_categories, list_accounts and
// get_reconciliation_status were all broken at once without a test noticing.
func TestMCPResultsAreObjects(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	calls := map[string]string{
		"list_budget_categories":    `{}`,
		"list_accounts":             `{}`,
		"get_reconciliation_status": `{"year":2026}`,
		"search_transactions":       `{"years":[2026]}`,
		"get_budget":                `{"period":"2026-08"}`,
		"get_actuals":               `{"month":"2026-08"}`,
	}
	for name, args := range calls {
		t.Run(name, func(t *testing.T) {
			w := mcpCall(t, s, apiTestToken,
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
			resp := decodeRPC(t, w.Body.String())
			result, ok := resp["result"].(map[string]any)
			if !ok {
				t.Fatalf("no result: %s", w.Body)
			}
			switch sc := result["structuredContent"].(type) {
			case nil, map[string]any:
			case []any:
				t.Errorf("structuredContent is a top-level array of %d, which MCP forbids", len(sc))
			default:
				t.Errorf("structuredContent is %T, want an object", sc)
			}
		})
	}
}

// TestMCPListsNameTheirCollection: wrapping a list in an object only helps if
// the key is the obvious one, since it is what a caller reads the result from.
func TestMCPListsNameTheirCollection(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	for tool, key := range map[string]string{
		"list_budget_categories":    "categories",
		"list_accounts":             "accounts",
		"get_reconciliation_status": "months",
	} {
		t.Run(tool, func(t *testing.T) {
			args := `{}`
			if tool == "get_reconciliation_status" {
				args = `{"year":2026}`
			}
			w := mcpCall(t, s, apiTestToken,
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+args+`}}`)
			resp := decodeRPC(t, w.Body.String())
			sc, _ := resp["result"].(map[string]any)["structuredContent"].(map[string]any)
			if _, ok := sc[key]; !ok {
				t.Errorf("%s result has keys %v, want one called %q", tool, keysOf(sc), key)
			}
		})
	}
}

// TestListAccountsCarriesTheKind: an agent picking an account for a statement
// line cannot infer from a name which pot the money is in, and that decides
// which side of the payroll cascade the line lands on. Both halves are checked
// because either alone is useless — a described field that is absent, or a
// present field nothing explains.
func TestListAccountsCarriesTheKind(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_accounts","arguments":{}}}`)
	resp := decodeRPC(t, w.Body.String())
	sc := resp["result"].(map[string]any)["structuredContent"].(map[string]any)
	accounts, ok := sc["accounts"].([]any)
	if !ok || len(accounts) != 2 {
		t.Fatalf("accounts = %v, want both", sc["accounts"])
	}
	got := map[string]string{}
	asOf := map[string]string{}
	for _, raw := range accounts {
		a := raw.(map[string]any)
		kind, _ := a["kind"].(string)
		got[a["name"].(string)] = kind
		date, _ := a["as_of"].(string)
		asOf[a["name"].(string)] = date
	}
	for name, want := range map[string]string{"Company Checking": "company", "Private Checking": "private"} {
		if got[name] != want {
			t.Errorf("%s kind = %q, want %q", name, got[name], want)
		}
	}
	// An account holds a series of readings now, so the one date reported has
	// to be the newest of them — an older one would understate how current the
	// balances are, which is the only thing this field is for.
	for name, want := range map[string]string{"Company Checking": "2026-07-31", "Private Checking": "2026-07-31"} {
		if asOf[name] != want {
			t.Errorf("%s as_of = %q, want %q — the newest reading", name, asOf[name], want)
		}
	}

	list := mcpCall(t, s, apiTestToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := decodeRPC(t, list.Body.String())["result"].(map[string]any)["tools"].([]any)
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] != "list_accounts" {
			continue
		}
		desc, _ := tool["description"].(string)
		for _, want := range []string{"kind", "company", "private", "as_of"} {
			if !strings.Contains(desc, want) {
				t.Errorf("list_accounts never mentions %q, so the field arrives unexplained:\n%s", want, desc)
			}
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestReadToolsStateTheirConsistency: a read that can lag is only safe if the
// caller is told so. Every read tool over data this API can write has to say
// what it guarantees, or an agent treats "not found" as "not there" and redoes
// work it already did.
func TestReadToolsStateTheirConsistency(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	w := mcpCall(t, s, apiTestToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp := decodeRPC(t, w.Body.String())

	desc := map[string]string{}
	for _, raw := range resp["result"].(map[string]any)["tools"].([]any) {
		tool := raw.(map[string]any)
		desc[tool["name"].(string)], _ = tool["description"].(string)
	}

	// Reads over data add_transactions, edit_transactions or
	// move_planned_expense can change.
	for _, name := range []string{"get_actuals", "get_budget", "search_transactions", "get_reconciliation_status"} {
		if !strings.Contains(desc[name], "deployed") {
			t.Errorf("%s never says when a change becomes visible:\n%s", name, desc[name])
		}
	}
	// The two that read data nothing here writes should not claim a caveat
	// they do not have.
	for _, name := range []string{"list_budget_categories", "list_accounts"} {
		if strings.Contains(desc[name], "EVENTUALLY CONSISTENT") {
			t.Errorf("%s claims to lag, but nothing in this API writes what it reads", name)
		}
	}
}
