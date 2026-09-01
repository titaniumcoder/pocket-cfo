package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The invoice documents here are hand-written pretty JSON, not marshalled
// structs, because the write path commits the caller's bytes with only the
// status member replaced — the exact formatting is part of what has to
// survive.

func draftInvoiceJSON(number, status, title string) string {
	return fmt.Sprintf(`{
  "schema_version": 1,
  "number": %q,
  "status": %q,

  "type": "invoice",
  "title": %q,
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
    {
      "description": { "de": "Beratung", "bg": "Консултации" },
      "unit_price": 15000,
      "vat_rate": 0
    }
  ],

  "tax": {
    "regime": "outside_eu_place_of_supply",
    "citations": ["ЗДДС чл. 69 ал. 2"],
    "note": { "de": "Hinweis", "bg": "Бележка" }
  }
}
`, number, status, title)
}

func draftRepoInvoices() map[string]string {
	return map[string]string{
		"data/invoices/INV-0000000001.json": draftInvoiceJSON("INV-0000000001", "issued", "Issued one"),
		"data/invoices/INV-0000000002.json": draftInvoiceJSON("INV-0000000002", "issued", "Issued two"),
		"data/invoices/INV-0000000003.json": draftInvoiceJSON("INV-0000000003", "draft", "The draft"),
	}
}

func draftService(t *testing.T, files map[string]string) (*Service, *fakeGitHub) {
	t.Helper()
	dataDir := t.TempDir()
	invoicesDir := filepath.Join(dataDir, "invoices")
	if err := os.MkdirAll(invoicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		if name, ok := strings.CutPrefix(path, "data/invoices/"); ok {
			if err := os.WriteFile(filepath.Join(invoicesDir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	gh := newFakeGitHub(files)
	srv := gh.server(t)
	s := &Service{
		InvoicesDir:      invoicesDir,
		PaidInvoicesPath: DefaultPaidInvoicesPath,
		CatalogPath:      "../../catalog/notes.json",
		Now:              func() time.Time { return time.Date(2026, time.February, 10, 9, 0, 0, 0, time.UTC) },
		Store:            &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL},
	}
	return s, gh
}

func saveDraft(t *testing.T, s *Service, number, status, title, baseSHA string) (*DraftSaveResult, error) {
	t.Helper()
	doc := draftInvoiceJSON(number, status, title)
	return s.SaveDraftInvoice(context.Background(), SaveDraftRequest{
		Document: json.RawMessage(doc), BaseSHA: baseSHA, Reason: "testing",
	})
}

func TestADraftUploadWithoutANumberCreatesTheNextInvoice(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	out, err := saveDraft(t, s, "", "draft", "A brand new one", "")
	if err != nil {
		t.Fatalf("SaveDraftInvoice: %v", err)
	}
	if !out.Created || out.Number != "INV-0000000004" {
		t.Fatalf("result = %+v, want INV-0000000004 created", out)
	}
	if !out.DeployPending {
		t.Error("a created draft should be deploy-pending")
	}
	written, ok := gh.files["data/invoices/INV-0000000004.json"]
	if !ok {
		t.Fatalf("nothing was committed: %v", gh.files)
	}
	if !strings.Contains(string(written), `"status": "draft"`) {
		t.Errorf("the committed file is not a draft:\n%s", written)
	}
	if !strings.Contains(gh.lastMsg, "feat(invoices): INV-0000000004 draft created") {
		t.Errorf("commit message = %q", gh.lastMsg)
	}
}

func TestANumberThatDoesNotExistIsRefusedWithTheWayOut(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	_, err := saveDraft(t, s, "INV-0000000009", "draft", "Ghost", "")
	if e, ok := err.(*Error); !ok || e.Code != CodeNotFound || !strings.Contains(e.Message, "leave the number out") {
		t.Fatalf("err = %v, want not_found telling the caller to omit the number", err)
	}
	if gh.puts != 0 {
		t.Errorf("%d commits were made", gh.puts)
	}
}

func TestAnIssuedInvoiceIsNeverEditableAgain(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	_, err := saveDraft(t, s, "INV-0000000001", "draft", "Rewritten", "")
	if e, ok := err.(*Error); !ok || !strings.Contains(e.Message, "never edited again") {
		t.Fatalf("err = %v, want the write-once refusal", err)
	}
	if gh.puts != 0 {
		t.Errorf("%d commits were made", gh.puts)
	}

	out, err := s.IssueInvoice(context.Background(), IssueInvoiceRequest{Invoice: "INV-0000000003", Reason: "it is ready"})
	if err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	if !out.DeployPending {
		t.Error("issuing should be deploy-pending")
	}
	if _, err := saveDraft(t, s, "INV-0000000003", "draft", "One more edit", ""); err == nil {
		t.Error("an edit was accepted after the draft flag was gone")
	}
}

func TestADraftCanBeSavedAgainAndAgain(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	for i, title := range []string{"Second pass", "Third pass", "Fourth pass"} {
		out, err := saveDraft(t, s, "INV-0000000003", "draft", title, "")
		if err != nil {
			t.Fatalf("pass %d: %v", i+2, err)
		}
		if out.Created {
			t.Errorf("pass %d reported a creation", i+2)
		}
	}
	if gh.puts != 3 {
		t.Errorf("%d commits, want 3 — every re-upload is a real edit", gh.puts)
	}
	written := string(gh.files["data/invoices/INV-0000000003.json"])
	if !strings.Contains(written, "Fourth pass") || !strings.Contains(written, `"status": "draft"`) {
		t.Errorf("the final file does not carry the last edit as a draft:\n%s", written)
	}
}

func TestAnUnchangedDraftUploadCommitsNothing(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	doc, err := s.InvoiceDocumentFor(context.Background(), "INV-0000000003")
	if err != nil {
		t.Fatalf("InvoiceDocumentFor: %v", err)
	}
	out, err := s.SaveDraftInvoice(context.Background(), SaveDraftInvoiceRequestFor(doc))
	if err != nil {
		t.Fatalf("SaveDraftInvoice: %v", err)
	}
	if out.DeployPending || out.SHA != doc.SHA {
		t.Errorf("result = %+v, want an idempotent no-op carrying the current sha", out)
	}
	if gh.puts != 0 {
		t.Errorf("%d commits were made", gh.puts)
	}
}

func SaveDraftInvoiceRequestFor(doc *InvoiceDocument) SaveDraftRequest {
	return SaveDraftRequest{Document: doc.Document}
}

func TestAnUploadClaimingIssuedIsCommittedAsADraft(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	if _, err := saveDraft(t, s, "INV-0000000003", "issued", "Sneaky", ""); err != nil {
		t.Fatalf("SaveDraftInvoice: %v", err)
	}
	written := string(gh.files["data/invoices/INV-0000000003.json"])
	if !strings.Contains(written, `"status": "draft"`) {
		t.Errorf("the upload issued itself:\n%s", written)
	}
	if !strings.Contains(written, "Sneaky") {
		t.Errorf("the rest of the document did not survive:\n%s", written)
	}
}

func TestAnInvalidDocumentIsRefusedBeforeAnythingIsCommitted(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	missingTitle := strings.Replace(draftInvoiceJSON("INV-0000000003", "draft", "The draft"), `"title": "The draft",`+"\n", "", 1)
	_, err := s.SaveDraftInvoice(context.Background(), SaveDraftRequest{Document: json.RawMessage(missingTitle)})
	if e, ok := err.(*Error); !ok || e.Code != CodeValidationFailed {
		t.Fatalf("err = %v, want validation_failed", err)
	}
	if gh.puts != 0 {
		t.Errorf("%d commits were made", gh.puts)
	}

	wrongNumber := strings.Replace(draftInvoiceJSON("", "draft", "Padded"), `""`, `" INV-0000000003 "`, 1)
	wrongNumber = strings.Replace(wrongNumber, `"number":  " INV-0000000003 "`, `"number": " INV-0000000003 "`, 1)
	_, err = s.SaveDraftInvoice(context.Background(), SaveDraftRequest{Document: json.RawMessage(wrongNumber)})
	if e, ok := err.(*Error); !ok || e.Code != CodeValidationFailed {
		t.Fatalf("err = %v, want the pattern refusal for the padded number", err)
	}
}

func TestAStaleBaseSHAConflictsAndCarriesTheCurrentOne(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	_, err := saveDraft(t, s, "INV-0000000003", "draft", "Based on a stale read", "sha-stale")
	if e, ok := err.(*Error); !ok || e.Code != CodeConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if gh.puts != 0 {
		t.Errorf("%d commits were made", gh.puts)
	}

	doc, err := s.InvoiceDocumentFor(context.Background(), "INV-0000000003")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveDraft(t, s, "INV-0000000003", "draft", "Based on a fresh read", doc.SHA); err != nil {
		t.Fatalf("the current sha was refused: %v", err)
	}
}

func TestIssuingFlipsOnlyTheStatus(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	before := gh.files["data/invoices/INV-0000000003.json"]
	if _, err := s.IssueInvoice(context.Background(), IssueInvoiceRequest{Invoice: "INV-0000000003", Reason: "sent to the client"}); err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	after := string(gh.files["data/invoices/INV-0000000003.json"])
	want := strings.Replace(string(before), `"status": "draft"`, `"status": "issued"`, 1)
	if after != want {
		t.Errorf("issuing rewrote more than the status line:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if !strings.Contains(gh.lastMsg, "feat(invoices): INV-0000000003 issued") {
		t.Errorf("commit message = %q", gh.lastMsg)
	}
}

func TestIssuingIsIdempotentAndRequiresAReason(t *testing.T) {
	s, gh := draftService(t, draftRepoInvoices())

	if _, err := s.IssueInvoice(context.Background(), IssueInvoiceRequest{Invoice: "INV-0000000003"}); err == nil {
		t.Error("issuing without a reason was accepted")
	}

	if _, err := s.IssueInvoice(context.Background(), IssueInvoiceRequest{Invoice: "INV-0000000003", Reason: "ready"}); err != nil {
		t.Fatalf("IssueInvoice: %v", err)
	}
	puts := gh.puts
	out, err := s.IssueInvoice(context.Background(), IssueInvoiceRequest{Invoice: "INV-0000000003", Reason: "ready"})
	if err != nil {
		t.Fatalf("re-issue: %v", err)
	}
	if out.DeployPending || gh.puts != puts {
		t.Errorf("re-issue = %+v with %d commits, want a no-op", out, gh.puts-puts)
	}
}

func TestADocumentReadReturnsTheCommittedBytes(t *testing.T) {
	s, _ := draftService(t, draftRepoInvoices())

	doc, err := s.InvoiceDocumentFor(context.Background(), "INV-0000000003")
	if err != nil {
		t.Fatalf("InvoiceDocumentFor: %v", err)
	}
	if doc.Status != "draft" || doc.Number != "INV-0000000003" {
		t.Errorf("doc = %+v", doc)
	}
	if string(doc.Document) != draftRepoInvoices()["data/invoices/INV-0000000003.json"] {
		t.Errorf("the document came back altered:\n%s", doc.Document)
	}
	if doc.SHA == "" {
		t.Error("no sha — an edit could not be based on anything")
	}
}

func TestAPDFVariantIsOnlyReachableInTheStateThatRendersIt(t *testing.T) {
	s, _ := draftService(t, draftRepoInvoices())

	target, err := s.InvoicePDFTarget(context.Background(), "INV-0000000003", "draft")
	if err != nil || target.Filename != "INV-0000000003-DRAFT.pdf" {
		t.Errorf("draft variant = %+v, %v", target, err)
	}
	if _, err := s.InvoicePDFTarget(context.Background(), "INV-0000000003", "original"); err == nil {
		t.Error("a draft served an original PDF")
	}
	target, err = s.InvoicePDFTarget(context.Background(), "INV-0000000002", "original")
	if err != nil || target.Filename != "INV-0000000002.pdf" {
		t.Errorf("original variant = %+v, %v", target, err)
	}
	if _, err := s.InvoicePDFTarget(context.Background(), "INV-0000000002", "draft"); err == nil {
		t.Error("an issued invoice served a draft PDF")
	}
	if _, err := s.InvoicePDFTarget(context.Background(), "INV-0000000002", "paid"); err == nil {
		t.Error("an unpaid invoice served a paid PDF")
	}
	if _, err := s.InvoicePDFTarget(context.Background(), "INV-0000000002", "inline"); err == nil {
		t.Error("an unknown variant was accepted")
	}
}

func TestAPaidPDFIsReachableOnceThePaymentIsRecorded(t *testing.T) {
	files := draftRepoInvoices()
	files["data/paid-invoices.json"] = `{
  "$schema": "../schemas/paid-invoices.json",
  "paid": [ { "invoice": "INV-0000000002", "date": "2026-02-01" } ]
}`
	s, _ := draftService(t, files)

	target, err := s.InvoicePDFTarget(context.Background(), "INV-0000000002", "paid")
	if err != nil || target.Filename != "INV-0000000002-paid.pdf" {
		t.Errorf("paid variant = %+v, %v", target, err)
	}
}

func TestTheInvoiceListNamesThePDFsItCanServe(t *testing.T) {
	s, _ := draftService(t, draftRepoInvoices())

	out, err := s.Invoices(context.Background(), "")
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	byNumber := map[string]Invoice{}
	for _, inv := range out.Invoices {
		byNumber[inv.Number] = inv
	}
	if got := byNumber["INV-0000000003"].PDFs; len(got) != 1 || got[0] != "draft" {
		t.Errorf("draft pdfs = %v, want [draft]", got)
	}
	if got := byNumber["INV-0000000001"].PDFs; len(got) != 1 || got[0] != "original" {
		t.Errorf("issued pdfs = %v, want [original]", got)
	}
}
