package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
)

func strp(v string) *string { return &v }

func mustDate(s string) types.SerializableDate {
	var d types.SerializableDate
	if err := d.UnmarshalJSON([]byte(`"` + s + `"`)); err != nil {
		panic(err)
	}
	return d
}

func TestComputeToken(t *testing.T) {
	a := computeToken(1, "passkey-a", "secret")
	b := computeToken(1, "passkey-a", "secret")
	if a != b {
		t.Errorf("computeToken is not deterministic: %q != %q", a, b)
	}
	if computeToken(1, "passkey-b", "secret") == a {
		t.Error("different passkeys produced the same token")
	}
	if computeToken(2, "passkey-a", "secret") == a {
		t.Error("different recipient numbers produced the same token")
	}
	if computeToken(1, "passkey-a", "other-secret") == a {
		t.Error("different secrets produced the same token")
	}
}

func TestFindRecipientByToken(t *testing.T) {
	recipients := []recipient.RecipientJson{
		{Number: 1, LegalName: "Alice Ltd", AccessPasskey: strp("alice-key")},
		{Number: 2, LegalName: "Bob GmbH"}, // no passkey set at all
	}

	t.Run("correct token matches", func(t *testing.T) {
		token := computeToken(1, "alice-key", "secret")
		got, ok := findRecipientByToken(token, recipients, "secret")
		if !ok || got.Number != 1 {
			t.Errorf("findRecipientByToken = %+v, %v, want Alice, true", got, ok)
		}
	})

	t.Run("wrong passkey does not match", func(t *testing.T) {
		token := computeToken(1, "wrong-key", "secret")
		if _, ok := findRecipientByToken(token, recipients, "secret"); ok {
			t.Error("expected no match for a wrong passkey")
		}
	})

	t.Run("recipient with no passkey has no working endpoint at all", func(t *testing.T) {
		// Even the "correct" token computed against an empty passkey must
		// not match — Bob has no access_passkey set, so there is no token
		// that should ever resolve to him.
		token := computeToken(2, "", "secret")
		if _, ok := findRecipientByToken(token, recipients, "secret"); ok {
			t.Error("expected no match for a recipient with no passkey set")
		}
	})
}

// invoiceFixture builds a minimal but schema-valid invoice for recipientNumber.
func invoiceFixture(number string, recipientNumber int, status invoice.InvoiceJsonStatus) invoice.InvoiceJson {
	return invoice.InvoiceJson{
		SchemaVersion: 1,
		Number:        number,
		Status:        status,
		Type:          "invoice",
		Title:         "Test invoice",
		IssueDate:     mustDate("2026-01-01"),
		DueDate:       mustDate("2026-01-15"),
		Currency:      "EUR",
		Language:      invoice.InvoiceJsonLanguageDe,
		Issuer: invoice.IssuerSnapshot{
			LegalName:       "Issuer Ltd",
			Address:         invoice.Address{Line1: "Street 1", PostalCode: "1000", City: "Sofia", CountryCode: "BG"},
			TaxId:           "123",
			VatId:           "BG123",
			Bank:            invoice.Bank{Name: "Bank", Iban: "IBAN", Bic: "BIC"},
			DefaultCurrency: "EUR",
		},
		Recipient: invoice.RecipientSnapshot{
			Number: recipientNumber, LegalName: "Recipient", IsBusiness: true,
			Language: invoice.RecipientSnapshotLanguageDe, PaymentTermsDays: 14, Email: "r@example.com",
			Address: invoice.Address{Line1: "Street 2", PostalCode: "2000", City: "Vienna", CountryCode: "AT"},
		},
		Lines: []invoice.Line{
			{Description: invoice.LocalizedString{De: strp("Arbeit"), Bg: strp("Работа")}, UnitPrice: 10000, VatRate: 0},
		},
		Tax: invoice.Tax{
			Regime:    invoice.TaxRegimeOutsideEuPlaceOfSupply,
			Citations: []string{"ЗДДС чл. 69 ал. 2"},
			Note:      invoice.LocalizedString{De: strp("Hinweis"), Bg: strp("Бележка")},
		},
	}
}

// writeFixtures lays down recipient/invoice/build data in the current
// (test-temp) directory: recipient 1 (Alice, has a passkey) has one issued
// invoice (INV-0000000001) and one draft (INV-0000000002); recipient 2 (no
// fixture recipient file needed) has an issued invoice (INV-0000000003)
// that must never be reachable through Alice's token.
func writeFixtures(t *testing.T) {
	t.Helper()

	mustMkdirAll(t, recipientsDir)
	mustMkdirAll(t, invoicesDir)
	mustMkdirAll(t, buildDir)

	writeJSON(t, filepath.Join(recipientsDir, "0001.json"), recipient.RecipientJson{
		Number: 1, LegalName: "Alice Ltd", Email: "alice@example.com", IsBusiness: true,
		Language: recipient.RecipientJsonLanguageDe, PaymentTermsDays: 14,
		Address:       recipient.Address{Line1: "Street 1", PostalCode: "1000", City: "Vienna", CountryCode: "AT"},
		AccessPasskey: strp("alice-key"),
	})

	writeJSON(t, filepath.Join(invoicesDir, "INV-0000000001.json"), invoiceFixture("INV-0000000001", 1, invoice.InvoiceJsonStatusIssued))
	writeJSON(t, filepath.Join(invoicesDir, "INV-0000000002.json"), invoiceFixture("INV-0000000002", 1, invoice.InvoiceJsonStatusDraft))
	writeJSON(t, filepath.Join(invoicesDir, "INV-0000000003.json"), invoiceFixture("INV-0000000003", 2, invoice.InvoiceJsonStatusIssued))

	mustWriteFile(t, filepath.Join(buildDir, "INV-0000000001.pdf"), "pdf-1")
	mustWriteFile(t, filepath.Join(buildDir, "INV-0000000002-DRAFT.pdf"), "pdf-2-draft")
	mustWriteFile(t, filepath.Join(buildDir, "INV-0000000003.pdf"), "pdf-3")
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, path, string(b))
}

// newTestClientServer builds a server with the real client.html template
// (resolved to an absolute path before the caller chdirs into a temp test
// fixture directory) and a fixed CLIENT_LINK_SECRET.
func newTestClientServer(t *testing.T) *server {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	templatePath := filepath.Join(wd, "..", "..", "web", "templates", "client.html")
	tmpl := template.Must(template.New("client.html").Funcs(templateFuncs).ParseFiles(templatePath))
	return &server{cfg: config{clientLinkSecret: "secret"}, clientTmpl: tmpl}
}

func TestHandleClientPortalDraftsExcluded(t *testing.T) {
	s := newTestClientServer(t)
	t.Chdir(t.TempDir())
	writeFixtures(t)

	token := computeToken(1, "alice-key", "secret")
	req := httptest.NewRequest(http.MethodGet, "/client/"+token, nil)
	req.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handleClientPortal(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("X-Robots-Tag = %q, want noindex", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "INV-0000000001") {
		t.Error("issued invoice missing from client portal")
	}
	if strings.Contains(body, "INV-0000000002") {
		t.Error("draft invoice must never appear in the client portal")
	}
	if strings.Contains(body, "INV-0000000003") {
		t.Error("another recipient's invoice must never appear")
	}
}

func TestHandleClientPortalUnknownTokenIs404(t *testing.T) {
	s := newTestClientServer(t)
	t.Chdir(t.TempDir())
	writeFixtures(t)

	req := httptest.NewRequest(http.MethodGet, "/client/no-such-token", nil)
	req.SetPathValue("token", "no-such-token")
	w := httptest.NewRecorder()
	s.handleClientPortal(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleClientInvoicePDF(t *testing.T) {
	s := newTestClientServer(t)
	t.Chdir(t.TempDir())
	writeFixtures(t)
	token := computeToken(1, "alice-key", "secret")

	t.Run("issued invoice belonging to this recipient is served", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/client/"+token+"/invoices/INV-0000000001.pdf", nil)
		req.SetPathValue("token", token)
		req.SetPathValue("file", "INV-0000000001.pdf")
		w := httptest.NewRecorder()
		s.handleClientInvoicePDF(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
		if got := w.Header().Get("X-Robots-Tag"); got != "noindex" {
			t.Errorf("X-Robots-Tag = %q, want noindex", got)
		}
	})

	t.Run("draft PDF is rejected outright", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/client/"+token+"/invoices/INV-0000000002-DRAFT.pdf", nil)
		req.SetPathValue("token", token)
		req.SetPathValue("file", "INV-0000000002-DRAFT.pdf")
		w := httptest.NewRecorder()
		s.handleClientInvoicePDF(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for a draft PDF", w.Code)
		}
	})

	t.Run("invoice belonging to a different recipient is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/client/"+token+"/invoices/INV-0000000003.pdf", nil)
		req.SetPathValue("token", token)
		req.SetPathValue("file", "INV-0000000003.pdf")
		w := httptest.NewRecorder()
		s.handleClientInvoicePDF(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for another recipient's invoice", w.Code)
		}
	})

	t.Run("unknown token is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/client/nope/invoices/INV-0000000001.pdf", nil)
		req.SetPathValue("token", "nope")
		req.SetPathValue("file", "INV-0000000001.pdf")
		w := httptest.NewRecorder()
		s.handleClientInvoicePDF(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for an unknown token", w.Code)
		}
	})
}
