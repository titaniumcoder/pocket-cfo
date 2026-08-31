package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
)

// writePaidFixture lays down paid-invoices.json beside the invoice fixtures.
const apiPaidInvoicesJSON = `{
  "$schema": "../schemas/paid-invoices.json",
  "paid": [
    { "invoice": "INV-0000000001", "date": "2026-02-01", "note": "received on the company account" }
  ]
}
`

func writePaidFixture(t *testing.T, paid string) {
	t.Helper()
	mustWriteFile(t, paidInvoicesPath, paid)
}

// TestAPIInvoiceListMatchesTheDashboard: the endpoint reads the same invoice
// files and paid list the dashboard does, so its numbers and states cannot
// drift from the page.
func TestAPIInvoiceListMatchesTheDashboard(t *testing.T) {
	writeFixtures(t)
	writePaidFixture(t, apiPaidInvoicesJSON)
	s := apiServer(t, apiTestToken, "prod")

	w := apiGet(t, s, "/api/invoices", apiTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var list struct {
		Years    []int         `json:"years"`
		Invoices []api.Invoice `json:"invoices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(list.Invoices) != 3 {
		t.Errorf("invoices = %d, want 3 (the draft included)", len(list.Invoices))
	}
	if len(list.Years) == 0 || list.Years[0] != 2026 {
		t.Errorf("years = %v, want [2026]", list.Years)
	}
	byNumber := map[string]api.Invoice{}
	for _, inv := range list.Invoices {
		byNumber[inv.Number] = inv
	}
	if paid := byNumber["INV-0000000001"]; paid.PaidOn != "2026-02-01" || paid.State != "paid" || paid.TotalCents != 10000 {
		t.Errorf("INV-0000000001 = %+v, want paid on 2026-02-01, total 10000 cents", paid)
	}
	if draft := byNumber["INV-0000000002"]; draft.State != "draft" {
		t.Errorf("draft state = %q, want draft", draft.State)
	}
	if open := byNumber["INV-0000000003"]; open.State != "issued" && open.State != "overdue" || open.PaidOn != "" {
		t.Errorf("INV-0000000003 = %+v, want an unpaid issued invoice", open)
	}
}

func TestAPIInvoiceListFiltersByYear(t *testing.T) {
	writeFixtures(t)
	writePaidFixture(t, `{"paid": []}`)
	s := apiServer(t, apiTestToken, "prod")

	w := apiGet(t, s, "/api/invoices?year=2019", apiTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var list struct {
		Invoices []api.Invoice `json:"invoices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(list.Invoices) != 0 {
		t.Errorf("year 2019 returned %d invoices, want none", len(list.Invoices))
	}
}

func TestAPIInvoicePaymentWithoutAWriteTokenIs503(t *testing.T) {
	writeFixtures(t)
	s := apiServer(t, apiTestToken, "prod") // no githubDataToken
	r := httptest.NewRequest(http.MethodPost, "/api/invoices/paid",
		strings.NewReader(`{"invoice":"INV-0000000001","paid":true,"date":"2026-02-09"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiTestToken)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body %s", w.Code, w.Body)
	}
	var e apiErrorBody
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error.Code != "write_not_configured" {
		t.Errorf("code = %q", e.Error.Code)
	}
}

func TestAPIInvoicePaymentRequiresABearer(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	r := httptest.NewRequest(http.MethodPost, "/api/invoices/paid", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAPIInvoicePaymentRejectsAnUnknownField(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	r := httptest.NewRequest(http.MethodPost, "/api/invoices/paid",
		strings.NewReader(`{"invoice":"INV-0000000001","paid":true,"date":"2026-02-09","typo":1}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiTestToken)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown field", w.Code)
	}
}

// TestMCPListInvoicesIsAnObjectWithItsInvoices: the tool and the endpoint read
// the same data, so both must report the fixture, wrapped under an invoices
// key — the top-level-array rule has bitten three tools at once before.
func TestMCPListInvoicesIsAnObjectWithAnInvoicesKey(t *testing.T) {
	writeFixtures(t)
	writePaidFixture(t, apiPaidInvoicesJSON)
	s := apiServer(t, apiTestToken, "prod")

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_invoices","arguments":{}}}`)
	resp := decodeRPC(t, w.Body.String())
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %s", w.Body)
	}
	if result["isError"] == true {
		t.Fatalf("list_invoices errored: %v", result)
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("structuredContent = %T, want an object", result["structuredContent"])
	}
	invoices, ok := sc["invoices"].([]any)
	if !ok {
		t.Errorf("structuredContent has no invoices list: %v", sc)
	}
	if len(invoices) != 3 {
		t.Errorf("invoices = %d, want 3", len(invoices))
	}
}

func TestMCPSetInvoicePaidReportsARefusal(t *testing.T) {
	writeFixtures(t)
	writePaidFixture(t, `{"paid": []}`)
	s := apiServer(t, apiTestToken, "prod") // no write token: the write is refused

	w := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"set_invoice_paid","arguments":{"invoice":"INV-0000000001","paid":true,"date":"2026-02-09"}}}`)
	resp := decodeRPC(t, w.Body.String())
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %s", w.Body)
	}
	if result["isError"] != true {
		t.Fatalf("set_invoice_paid without a write token did not error: %v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("the error carries no content: %v", result)
	}
	b, _ := json.Marshal(result["content"])
	if !strings.Contains(string(b), "write_not_configured") {
		t.Errorf("the error does not carry the code: %s", b)
	}
}
