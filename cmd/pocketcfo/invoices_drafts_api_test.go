package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/api"
)

// draftInvoiceBody is a valid invoice document: a Swiss business recipient
// under outside_eu_place_of_supply, like the reference invoices, so it passes
// the real validators with the real catalog.
func draftDocumentForAPI(number, status, title string) string {
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

// withRealCatalog points CATALOG_DIR at this repo's accountant-owned catalog.
func withRealCatalog(t *testing.T) {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_DIR", filepath.Join(root, "..", "..", "catalog"))
}

func apiPost(t *testing.T, s *server, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func TestAPIInvoiceDocumentServesTheWholeDocument(t *testing.T) {
	writeFixtures(t)
	s := apiServer(t, apiTestToken, "prod")

	w := apiGet(t, s, "/api/invoices/INV-0000000002/document", apiTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var doc api.InvoiceDocument
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body: %v", err)
	}
	if doc.Number != "INV-0000000002" || doc.Status != "draft" {
		t.Errorf("doc = %+v", doc)
	}
	if !strings.Contains(string(doc.Document), "INV-0000000002") {
		t.Errorf("the document itself is missing: %.200s", doc.Document)
	}
	if w := apiGet(t, s, "/api/invoices/INV-0000000999/document", apiTestToken); w.Code != http.StatusNotFound {
		t.Errorf("unknown number = %d, want 404", w.Code)
	}
	if w := apiGet(t, s, "/api/invoices/INV-0000000002/document", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no bearer = %d, want 401", w.Code)
	}
}

func TestAPIInvoicePDFServesEachVariantOnlyInItsState(t *testing.T) {
	writeFixtures(t)
	writePaidFixture(t, apiPaidInvoicesJSON)
	mustWriteFile(t, filepath.Join(buildDir, "INV-0000000001-paid.pdf"), "pdf-1-paid")
	s := apiServer(t, apiTestToken, "prod")

	w := apiGet(t, s, "/api/invoices/INV-0000000002/pdf?variant=draft", apiTestToken)
	if w.Code != http.StatusOK || w.Body.String() != "pdf-2-draft" {
		t.Errorf("draft variant = %d %q, want the draft PDF", w.Code, w.Body)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "INV-0000000002-DRAFT.pdf") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/pdf") {
		t.Errorf("Content-Type = %q", ct)
	}

	if w := apiGet(t, s, "/api/invoices/INV-0000000002/pdf?variant=original", apiTestToken); w.Code != http.StatusBadRequest {
		t.Errorf("original of a draft = %d, want 400", w.Code)
	}
	w = apiGet(t, s, "/api/invoices/INV-0000000001/pdf?variant=original", apiTestToken)
	if w.Code != http.StatusOK || w.Body.String() != "pdf-1" {
		t.Errorf("original variant = %d %q", w.Code, w.Body)
	}
	if w := apiGet(t, s, "/api/invoices/INV-0000000002/pdf?variant=paid", apiTestToken); w.Code != http.StatusBadRequest {
		t.Errorf("paid variant of an unpaid draft = %d, want 400", w.Code)
	}
	w = apiGet(t, s, "/api/invoices/INV-0000000001/pdf?variant=paid", apiTestToken)
	if w.Code != http.StatusOK || w.Body.String() != "pdf-1-paid" {
		t.Errorf("paid variant = %d %q", w.Code, w.Body)
	}
	mustWriteFile(t, filepath.Join(buildDir, "INV-0000000003.pdf"), "pdf-3")
	if err := os.Remove(filepath.Join(buildDir, "INV-0000000003.pdf")); err != nil {
		t.Fatal(err)
	}
	if w := apiGet(t, s, "/api/invoices/INV-0000000003/pdf?variant=original", apiTestToken); w.Code != http.StatusNotFound {
		t.Errorf("missing render = %d, want 404", w.Code)
	} else if !strings.Contains(w.Body.String(), "not in the build output yet") {
		t.Errorf("the 404 does not explain the deploy lag: %s", w.Body)
	}
	if w := apiGet(t, s, "/api/invoices/INV-0000000001/pdf?variant=paid", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no bearer = %d, want 401", w.Code)
	}
}

func TestAPISaveInvoiceDraftCreatesThroughTheWholeStack(t *testing.T) {
	withRealCatalog(t)
	s, gh := writingServer(t, nil)

	body := `{"document": ` + draftDocumentForAPI("", "draft", "A fresh one") + `,"reason": "January support"}`
	w := apiPost(t, s, "/api/invoices/draft", body, apiTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var out api.DraftSaveResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.Number != "INV-0000000001" {
		t.Fatalf("result = %+v, want INV-0000000001 created", out)
	}
	written, ok := gh.files["data/invoices/INV-0000000001.json"]
	if !ok || !strings.Contains(string(written), `"status": "draft"`) {
		t.Errorf("nothing usable was committed: %s", written)
	}

	doc := apiGet(t, s, "/api/invoices/INV-0000000001/document", apiTestToken)
	if doc.Code != http.StatusOK {
		t.Fatalf("read-back = %d, body %s", doc.Code, doc.Body)
	}

	update := `{"document": ` + draftDocumentForAPI("INV-0000000001", "draft", "A fresh one, revised") + `}`
	w = apiPost(t, s, "/api/invoices/draft", update, apiTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("re-upload = %d, body %s", w.Code, w.Body)
	}
	issued := `{"document": ` + draftDocumentForAPI("INV-0000000002", "draft", "Ghost") + `}`
	if w := apiPost(t, s, "/api/invoices/draft", issued, apiTestToken); w.Code != http.StatusNotFound {
		t.Errorf("upload to a number that does not exist = %d, want 404", w.Code)
	}
}

func TestAPIIssueInvoiceFreezesTheDocument(t *testing.T) {
	withRealCatalog(t)
	s, gh := writingServer(t, map[string]string{
		"data/invoices/INV-0000000001.json": draftDocumentForAPI("INV-0000000001", "draft", "The draft"),
	})

	if w := apiPost(t, s, "/api/invoices/issue", `{"invoice":"INV-0000000001"}`, apiTestToken); w.Code != http.StatusBadRequest {
		t.Errorf("issue without a reason = %d, want 400", w.Code)
	}

	w := apiPost(t, s, "/api/invoices/issue", `{"invoice":"INV-0000000001","reason":"sent"}`, apiTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if issued := string(gh.files["data/invoices/INV-0000000001.json"]); !strings.Contains(issued, `"status": "issued"`) {
		t.Errorf("nothing was issued:\n%s", issued)
	}

	doc := apiGet(t, s, "/api/invoices/INV-0000000001/document", apiTestToken)
	if !strings.Contains(doc.Body.String(), `"status":"issued"`) {
		t.Errorf("read-back = %s", doc.Body)
	}

	redraft := `{"document": ` + draftDocumentForAPI("INV-0000000001", "draft", "One more edit") + `}`
	w = apiPost(t, s, "/api/invoices/draft", redraft, apiTestToken)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "never edited again") {
		t.Errorf("post-issue upload = %d %s, want the write-once refusal", w.Code, w.Body)
	}
}

func TestTheDraftRoutesAreGatedLikeEveryOtherWrite(t *testing.T) {
	writeFixtures(t)
	s := apiServer(t, apiTestToken, "prod")

	if w := apiPost(t, s, "/api/invoices/draft", `{}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("draft without a bearer = %d, want 401", w.Code)
	}
	if w := apiPost(t, s, "/api/invoices/issue", `{}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("issue without a bearer = %d, want 401", w.Code)
	}
	if w := apiPost(t, s, "/api/invoices/draft", `{"document":{}}`, apiTestToken); w.Code != http.StatusServiceUnavailable {
		t.Errorf("draft with no write token = %d, want 503", w.Code)
	}
}
