package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
		"move_planned_expense": true, "schedule_amount_change": true,
		"record_account_balance": true, "get_finance_config": true, "get_director_loan": true,
		"list_invoices": true, "set_invoice_paid": true,
		"get_invoice_document": true, "save_draft_invoice": true, "issue_invoice": true,
		"derive_transaction_ids": true, "read_data_file": true, "write_data_file": true,
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
		// That the date is a month end and mid-month is refused, which month
		// the figure opens, that it is appended rather than written over, and
		// that an account is never invented.
		"record_account_balance": {
			"mid-month balances are not allowed", "last day of its month",
			"closing balance", "opens", "appended", "never creates an account",
		},
		// That only a future month may be planned — an already closed budget
		// is fixed in the file — that one-offs are out of reach, that a
		// correction is a wholesale replacement, and that no negative figure
		// or an over-tall minimal can sneak in.
		"schedule_amount_change": {
			"must be in the future", "already closed budget is fixed in budget.json",
			"refuses one-offs", "amount_changes", "replaces that month's entry as a whole",
			"never negative", "cannot exceed the amount it reduces",
		},
		// That the upload can never change an invoice's state, that the number
		// is assigned on creation, that an issued invoice is out of reach
		// forever, and that a re-send is safe.
		"save_draft_invoice": {
			"never changes an invoice's state", "a draft stays a draft",
			"number is assigned", "refused forever", "write-once", "commits nothing",
		},
		// That issuing is the one direction the flag moves, that the diff is
		// the status line alone, and that the freeze is permanent.
		"issue_invoice": {
			"only in that direction", "the status line and nothing else",
			"freezes the document forever", "idempotent no-op",
		},
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
			if path != "../.." && (strings.HasPrefix(info.Name(), ".") || info.Name() == "build") {
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

	// Reads over data add_transactions, edit_transactions,
	// move_planned_expense, schedule_amount_change or record_account_balance
	// can change.
	for _, name := range []string{"get_actuals", "get_budget", "search_transactions", "get_reconciliation_status", "list_accounts"} {
		if !strings.Contains(desc[name], "deployed") {
			t.Errorf("%s never says when a change becomes visible:\n%s", name, desc[name])
		}
	}
	// The one that reads data nothing here writes should not claim a caveat it
	// does not have: no tool creates, renames or removes a category.
	if strings.Contains(desc["list_budget_categories"], "EVENTUALLY CONSISTENT") {
		t.Error("list_budget_categories claims to lag, but nothing in this API writes what it reads")
	}
}

// TestMCPAndRESTAgreeOnRefusingAMidMonthBalance: the month-end rule is the
// whole contract of record_account_balance, so it has to be the service that
// holds it — in our words, on both surfaces. If the SDK's schema layer refused
// it first, MCP would answer with a validation error nobody can act on, and
// the rule would be untested on the path Hermes actually takes.
func TestMCPAndRESTAgreeOnRefusingAMidMonthBalance(t *testing.T) {
	body := `{"account":"Private Checking","as_of":"2026-08-14","balance":1200}`

	s := putServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/balance", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+apiTestToken)
	r.Header.Set("Content-Type", "application/json")
	rest := httptest.NewRecorder()
	s.routes().ServeHTTP(rest, r)

	if rest.Code != http.StatusBadRequest {
		t.Errorf("REST status = %d, want 400", rest.Code)
	}
	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record_account_balance","arguments":`+body+`}}`)

	const reason = "mid-month"
	if !strings.Contains(rest.Body.String(), reason) {
		t.Errorf("REST said %s, want the mid-month refusal", rest.Body)
	}
	if !strings.Contains(w.Body.String(), reason) {
		t.Errorf("MCP said %s, want the mid-month refusal — a schema error means the SDK refused it first", w.Body)
	}
}

// fakeContents is a GitHub Contents API just good enough to commit through:
// the accepted write path is proved in internal/api, and what this pins is
// that the whole stack in between — SDK decoding, the service, the store —
// carries a real call from JSON-RPC to a commit.
type fakeContents struct {
	files   map[string][]byte
	blobs   map[string][]byte
	deletes []string
	puts    int
}

func contentSHA(body []byte) string { return "sha-" + fmt.Sprintf("%x", sha256.Sum256(body))[:12] }

func (f *fakeContents) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/{owner}/{repo}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		switch r.Method {
		case http.MethodGet:
			if body, ok := f.files[path]; ok {
				json.NewEncoder(w).Encode(map[string]string{
					"content": base64.StdEncoding.EncodeToString(body),
					"sha":     contentSHA(body),
				})
				return
			}
			var entries []map[string]string
			for name := range f.files {
				if rel, ok := strings.CutPrefix(name, path+"/"); ok && !strings.Contains(rel, "/") {
					entries = append(entries, map[string]string{"name": rel, "type": "file"})
				}
			}
			if entries != nil {
				json.NewEncoder(w).Encode(entries)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			var req struct{ Content string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("PUT body is not JSON: %v", err)
			}
			decoded, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				t.Fatalf("PUT content is not base64: %v", err)
			}
			f.files[path] = decoded
			f.puts++
			if f.blobs == nil {
				f.blobs = map[string][]byte{}
			}
			f.blobs[contentSHA(decoded)] = decoded
			json.NewEncoder(w).Encode(map[string]any{"content": map[string]string{"sha": contentSHA(decoded)}})
		case http.MethodDelete:
			var req struct{ SHA string }
			json.NewDecoder(r.Body).Decode(&req)
			if body, ok := f.files[path]; !ok || contentSHA(body) != req.SHA {
				w.WriteHeader(http.StatusConflict)
				return
			}
			delete(f.files, path)
			f.deletes = append(f.deletes, path)
			io.WriteString(w, "{}")
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.blobs[r.PathValue("sha")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString(body), "encoding": "base64"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writingServer(t *testing.T, files map[string]string) (*server, *fakeContents) {
	t.Helper()
	gh := &fakeContents{files: map[string][]byte{}, blobs: map[string][]byte{}}
	for k, v := range files {
		gh.files[k] = []byte(v)
		gh.blobs[contentSHA([]byte(v))] = []byte(v)
	}
	srv := gh.serve(t)
	s := putServer(t)
	s.cfg.githubAPIURL = srv.URL
	s.httpClient = srv.Client()
	return s, gh
}

// TestMCPRecordsABalanceThroughTheWholeStack drives the accepted path over the
// wire: the tool is reachable, the arguments survive the SDK's decoding, and
// what comes back names the month the figure opens rather than the one it
// closed — the distinction the whole rule exists for.
func TestMCPRecordsABalanceThroughTheWholeStack(t *testing.T) {
	s, gh := writingServer(t, map[string]string{
		"data/accounts.json": `{"accounts":[
  {"name":"Private Checking","kind":"private","balances":[{"as_of":"2026-07-31","balance":100}]}
]}`,
	})

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record_account_balance",`+
			`"arguments":{"account":"Private Checking","as_of":"2026-07-31","balance":90}}}`)
	if !strings.Contains(w.Body.String(), "conflict") {
		t.Errorf("July was already read, so a second reading must conflict: %s", w.Body)
	}

	w = mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"record_account_balance",`+
			`"arguments":{"account":"Private Checking","as_of":"2026-06-30","balance":90.5,"note":"read late"}}}`)
	resp := decodeRPC(t, w.Body.String())
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %s", w.Body)
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want an object", result["structuredContent"])
	}
	if sc["opens"] != "2026-07" || sc["closes"] != "2026-06" {
		t.Errorf("closes/opens = %v/%v, want 2026-06/2026-07", sc["closes"], sc["opens"])
	}
	if sc["kind"] != "private" {
		t.Errorf("kind = %v, want private", sc["kind"])
	}
	written := string(gh.files["data/accounts.json"])
	if !strings.Contains(written, `{ "as_of": "2026-06-30", "balance": 90.5, "note": "read late" }`) {
		t.Errorf("the reading was not committed as written:\n%s", written)
	}
}

// changeBudgetJSON is the file the GitHub stub serves: a flat rent, so the
// call has a plain category to step, plus a one-off a mistake could bite on.
const changeBudgetJSON = `{
  "$schema": "../internal/finance/data/budget.schema.json",
  "groups": [
    { "name": "Housing", "kind": "private", "categories": [
      { "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 900 }
    ]}
  ]
}`

func changeServer(t *testing.T) (*server, *fakeContents) {
	return writingServer(t, map[string]string{"data/budget.json": changeBudgetJSON})
}

// TestMCPAndRESTAgreeOnRefusingAStepOnAClosedMonth: the future-only rule is
// the whole safety case of schedule_amount_change, so both surfaces have to
// hold it in our words. A month that is already in force is a closed budget;
// the answer points at budget.json rather than offering to fix it here.
func TestMCPAndRESTAgreeOnRefusingAStepOnAClosedMonth(t *testing.T) {
	s, _ := changeServer(t)
	thisMonth := time.Now().UTC().Format("2006-01")
	body := `{"category_id":"00000000-0000-4000-8000-000000000001","from_month":` +
		`"` + thisMonth + `","amount":999,"reason":"late raise","base_sha":"` + contentSHA([]byte(changeBudgetJSON)) + `"}`

	r := httptest.NewRequest(http.MethodPost, "/api/budget/schedule-amount-change", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+apiTestToken)
	r.Header.Set("Content-Type", "application/json")
	rest := httptest.NewRecorder()
	s.routes().ServeHTTP(rest, r)
	if rest.Code != http.StatusBadRequest {
		t.Errorf("REST status = %d, want 400: %s", rest.Code, rest.Body)
	}

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"schedule_amount_change","arguments":`+body+`}}`)

	const reason = "already in force"
	if !strings.Contains(rest.Body.String(), reason) {
		t.Errorf("REST said %s, want the closed-month refusal", rest.Body)
	}
	if !strings.Contains(w.Body.String(), reason) {
		t.Errorf("MCP said %s, want the closed-month refusal", w.Body)
	}
}

// TestMCPSchedulesAnAmountChangeThroughTheWholeStack: the accepted path over
// the wire — the tool is reachable, the arguments survive the SDK's
// decoding, and the committed budget.json carries the new step while every
// other line of the file stays where it was.
func TestMCPSchedulesAnAmountChangeThroughTheWholeStack(t *testing.T) {
	s, gh := changeServer(t)
	nextJanuary := time.Now().UTC().AddDate(1, 0, 0).Format("2006-01")

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"schedule_amount_change","arguments":`+
			`{"category_id":"00000000-0000-4000-8000-000000000001","from_month":"`+nextJanuary+`"`+
			`,"amount":950,"minimal_amount":900,"reason":"the landlord put the rent up","base_sha":"`+contentSHA([]byte(changeBudgetJSON))+`"}}}`)
	resp := decodeRPC(t, w.Body.String())
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %s", w.Body)
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want an object", result["structuredContent"])
	}
	if sc["name"] != "Rent" || sc["from"] != nextJanuary || sc["amount"] != 950.0 {
		t.Errorf("result = %v, want Rent stepping to 950 in %s", sc, nextJanuary)
	}
	if sc["sha"] != contentSHA(gh.files["data/budget.json"]) {
		t.Errorf("sha = %v, want the committed content's sha", sc["sha"])
	}

	written := string(gh.files["data/budget.json"])
	entry := `{ "from": "` + nextJanuary + `-01", "amount": 950, "minimal_amount": 900 }`
	if !strings.Contains(written, `"amount_changes": [ `+entry+` ]`) {
		t.Errorf("the step was not committed as written:\n%s", written)
	}
	if !strings.Contains(written, `{ "id": "00000000-0000-4000-8000-000000000001", "name": "Rent", "amount": 900,`) {
		t.Errorf("the base amount or the category's other lines moved:\n%s", written)
	}
	var doc map[string]any
	if err := json.Unmarshal(gh.files["data/budget.json"], &doc); err != nil {
		t.Fatalf("the committed budget.json does not parse: %v", err)
	}
}

// draftInvoiceMCP is a valid invoice document for the draft tools: a Swiss
// business recipient under outside_eu_place_of_supply, like the reference
// invoices, so it passes the real validators with the real catalog.
func draftInvoiceForMCP(number, status, title string) string {
	numberField := ""
	if number != "" {
		numberField = `"number": "` + number + `",`
	}
	return `{
  "schema_version": 1,
  ` + numberField + `
  "status": "` + status + `",
  "type": "invoice",
  "title": "` + title + `",
  "issue_date": "2026-01-15",
  "due_date": "2026-02-15",
  "currency": "EUR",
  "language": "de",
  "issuer": {
    "legal_name": "Example Issuer EOOD",
    "address": { "line1": "Musterstraße 1", "postal_code": "9000", "city": "Varna", "country_code": "BG" },
    "tax_id": "000000000",
    "vat_id": "BG000000000",
    "bank": { "name": "Example Bank", "iban": "DE89370400440532013000", "bic": "COBADEFFXXX" },
    "default_currency": "EUR"
  },
  "recipient": {
    "number": 7,
    "legal_name": "Musterfirma GmbH",
    "address": { "line1": "Musterweg 1", "postal_code": "8000", "city": "Zürich", "country_code": "CH" },
    "is_business": true,
    "language": "de",
    "payment_terms_days": 30,
    "email": "buchhaltung@musterfirma.example"
  },
  "lines": [
    { "description": { "de": "Beratung", "bg": "Консултации" }, "unit_price": 15000, "vat_rate": 0 }
  ],
  "tax": {
    "regime": "outside_eu_place_of_supply",
    "citations": ["ЗДДС чл. 69 ал. 2"],
    "note": { "de": "Hinweis", "bg": "Бележка" }
  }
}`
}

// TestMCPSavesADraftThroughTheWholeStack: the draft loop over the wire — the
// document survives the SDK's decoding as a string argument, the number is
// assigned by the service, and the committed file is the uploaded bytes with
// the status alone rewritten to draft.
func TestMCPSavesADraftThroughTheWholeStack(t *testing.T) {
	withRealCatalog(t)
	s, gh := writingServer(t, nil)

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_draft_invoice","arguments":{"document":`+
			strconv.Quote(draftInvoiceForMCP("", "draft", "January support"))+`,"reason":"January support"}}}`)
	resp := decodeRPC(t, w.Body.String())
	sc, ok := resp["result"].(map[string]any)["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent in %s", w.Body)
	}
	if sc["number"] != "INV-0000000001" || sc["created"] != true {
		t.Fatalf("result = %v, want INV-0000000001 created", sc)
	}
	written := string(gh.files["data/invoices/INV-0000000001.json"])
	if !strings.Contains(written, `"status": "draft"`) {
		t.Errorf("the committed file is not a draft:\n%s", written)
	}
	// And the read tool serves it back with the same bytes the service wrote.
	w = mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_invoice_document","arguments":{"number":"INV-0000000001"}}}`)
	sc = decodeRPC(t, w.Body.String())["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc["status"] != "draft" {
		t.Errorf("read-back status = %v", sc["status"])
	}
	doc, _ := json.Marshal(sc["document"])
	if !strings.Contains(string(doc), "January support") {
		t.Errorf("the document did not survive the read-back: %.200s", doc)
	}
}

// TestMCPIsuesAnInvoiceThroughTheWholeStack: the commit carries the issued
// status and every other byte of the file is untouched, and a further upload
// to the number is refused for good.
func TestMCPIsuesAnInvoiceThroughTheWholeStack(t *testing.T) {
	withRealCatalog(t)
	s, gh := writingServer(t, map[string]string{
		"data/invoices/INV-0000000001.json": draftInvoiceForMCP("INV-0000000001", "draft", "The draft"),
	})

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"issue_invoice","arguments":{"invoice":"INV-0000000001","reason":"sent to the client"}}}`)
	resp := decodeRPC(t, w.Body.String())
	if _, ok := resp["result"].(map[string]any)["structuredContent"].(map[string]any); !ok {
		t.Fatalf("no structuredContent in %s", w.Body)
	}
	before := draftInvoiceForMCP("INV-0000000001", "draft", "The draft")
	after := string(gh.files["data/invoices/INV-0000000001.json"])
	if after != strings.Replace(before, `"status": "draft"`, `"status": "issued"`, 1) {
		t.Errorf("issuing rewrote more than the status line:\n%s", after)
	}

	w = mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_draft_invoice","arguments":{"document":`+
			strconv.Quote(draftInvoiceForMCP("INV-0000000001", "draft", "One more edit"))+`}}}`)
	if !strings.Contains(w.Body.String(), "never edited again") {
		t.Errorf("post-issue upload = %s, want the write-once refusal", w.Body)
	}
}
