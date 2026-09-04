package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const idsBody = `{"lines":[
	{"account":"Private Checking","date":"2026-08-14","amount":12.5,"description":"PARKMART 0042"},
	{"account":"Private Checking","date":"2026-08-14","amount":12.5,"description":"PARKMART 0042"},
	{"account":"Company Checking","date":"2026-08-02","amount":-40,"description":"REFUND"}
]}`

func TestMCPAndRESTAgreeOnDerivedIDs(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")

	r := httptest.NewRequest(http.MethodPost, "/api/actuals/ids", strings.NewReader(idsBody))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiTestToken)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("REST status = %d, body %s", w.Code, w.Body)
	}
	var rest struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rest); err != nil {
		t.Fatal(err)
	}
	if len(rest.IDs) != 3 || rest.IDs[1] != rest.IDs[0]+"-2" || rest.IDs[2] == rest.IDs[0] {
		t.Fatalf("REST ids = %v", rest.IDs)
	}

	m := mcpCall(t, s, apiTestToken,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"derive_transaction_ids","arguments":`+idsBody+`}}`)
	if m.Code != http.StatusOK {
		t.Fatalf("MCP status = %d, body %s", m.Code, m.Body)
	}
	result := decodeRPC(t, m.Body.String())["result"].(map[string]any)
	got := result["structuredContent"].(map[string]any)["ids"].([]any)
	for i, id := range rest.IDs {
		if got[i] != id {
			t.Errorf("MCP id %d = %v, REST says %s", i, got[i], id)
		}
	}
}

func TestDerivedIDsRefuseAnUnknownDateShape(t *testing.T) {
	s := apiServer(t, apiTestToken, "prod")
	r := httptest.NewRequest(http.MethodPost, "/api/actuals/ids", strings.NewReader(`{"lines":[{"account":"a","date":"14.08.2026","amount":1,"description":"x"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiTestToken)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_request") {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
}
