package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
)

// Two issued invoices and one draft, all issued 2026-01-15. paid-invoices.json
// marks INV-0000000001 paid on 2026-02-01.
const paidInvoicesFixture = `{
  "$schema": "../schemas/paid-invoices.json",
  "paid": [
    { "invoice": "INV-0000000001", "date": "2026-02-01" }
  ]
}
`

func invoiceFixtureForAPI(number string, status invoice.InvoiceJsonStatus, issueDate string) invoice.InvoiceJson {
	return invoice.InvoiceJson{
		SchemaVersion: 1, Number: number, Status: status, Type: "invoice",
		Title: "Consulting", IssueDate: serialDate(issueDate), DueDate: serialDate("2026-03-01"),
		Currency: "EUR", Language: invoice.InvoiceJsonLanguageEn,
		Issuer: invoice.IssuerSnapshot{
			LegalName: "Issuer GmbH", TaxId: "BG123", VatId: "BG456", DefaultCurrency: "EUR",
			Address: invoice.Address{Line1: "Street 1", PostalCode: "1000", City: "Sofia", CountryCode: "BG"},
			Bank:    invoice.Bank{Name: "Bank", Iban: "IBAN", Bic: "BIC"},
		},
		Recipient: invoice.RecipientSnapshot{
			Number: 1, LegalName: "Recipient GmbH", Language: invoice.RecipientSnapshotLanguageDe,
			PaymentTermsDays: 14, Email: "r@example.com",
			Address: invoice.Address{Line1: "Street 2", PostalCode: "2000", City: "Vienna", CountryCode: "AT"},
		},
		Lines: []invoice.Line{{Description: invoice.LocalizedString{De: strp("Arbeit")}, UnitPrice: 15000, VatRate: 0}},
		Tax:   invoice.Tax{Regime: invoice.TaxRegimeOutsideEuPlaceOfSupply},
	}
}

func serialDate(s string) types.SerializableDate {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return types.SerializableDate{Time: t}
}

func invoiceService(t *testing.T, gh *fakeGitHub) *Service {
	t.Helper()
	dataDir := t.TempDir()
	invoicesDir := filepath.Join(dataDir, "invoices")
	if err := os.MkdirAll(invoicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for number, status := range map[string]invoice.InvoiceJsonStatus{
		"INV-0000000001": invoice.InvoiceJsonStatusIssued,
		"INV-0000000002": invoice.InvoiceJsonStatusIssued,
		"INV-0000000003": invoice.InvoiceJsonStatusDraft,
	} {
		b, err := json.Marshal(invoiceFixtureForAPI(number, status, "2026-01-15"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(invoicesDir, number+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{
		InvoicesDir:      invoicesDir,
		PaidInvoicesPath: DefaultPaidInvoicesPath,
		Now:              func() time.Time { return time.Date(2026, time.February, 10, 9, 0, 0, 0, time.UTC) },
	}
	if gh != nil {
		srv := gh.server(t)
		s.Store = &ContentsClient{HTTP: srv.Client(), Repo: "owner/data", Token: "test-token", BaseURL: srv.URL}
	}
	return s
}

func TestTheInvoiceListMatchesTheDashboard(t *testing.T) {
	s := invoiceService(t, nil)
	s.PaidInvoicesPath = filepath.Join(filepath.Dir(s.InvoicesDir), "paid-invoices.json")
	older := invoiceFixtureForAPI("INV-0000000009", invoice.InvoiceJsonStatusIssued, "2025-11-20")
	b, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.InvoicesDir, "INV-0000000009.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.PaidInvoicesPath, []byte(paidInvoicesFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.Invoices(context.Background(), "")
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	if len(got.Invoices) != 4 {
		t.Errorf("invoices = %d, want 4 (drafts included): %+v", len(got.Invoices), got.Invoices)
	}
	if len(got.Years) != 2 || got.Years[0] != 2026 || got.Years[1] != 2025 {
		t.Errorf("years = %v, want [2026 2025]", got.Years)
	}
	byNumber := map[string]Invoice{}
	for _, inv := range got.Invoices {
		byNumber[inv.Number] = inv
	}
	paid := byNumber["INV-0000000001"]
	if paid.State != "paid" || paid.PaidOn != "2026-02-01" || paid.TotalCents != 15000 {
		t.Errorf("INV-0000000001 = %+v, want paid on 2026-02-01, total 15000 cents", paid)
	}
	if open := byNumber["INV-0000000002"]; open.State != "issued" || open.PaidOn != "" {
		t.Errorf("INV-0000000002 = %+v, want an unpaid issued invoice", open)
	}
	if byNumber["INV-0000000003"].State != "draft" {
		t.Errorf("draft state = %q, want draft", byNumber["INV-0000000003"].State)
	}
	if _, ok := byNumber["INV-0000000009"]; !ok {
		t.Errorf("the 2025 invoice is missing from the full list")
	}

	only2025, err := s.Invoices(context.Background(), "2025")
	if err != nil {
		t.Fatalf("Invoices(2025): %v", err)
	}
	if len(only2025.Invoices) != 1 || only2025.Invoices[0].Number != "INV-0000000009" {
		t.Errorf("Invoices(2025) = %+v, want only INV-0000000009", only2025.Invoices)
	}
}

func TestAnUnpaidInvoicePastItsDueDateReadsAsOverdue(t *testing.T) {
	s := invoiceService(t, nil)
	overdue := invoiceFixtureForAPI("INV-0000000004", invoice.InvoiceJsonStatusIssued, "2025-11-20")
	overdue.DueDate = serialDate("2025-12-15")
	b, err := json.Marshal(overdue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.InvoicesDir, "INV-0000000004.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.Invoices(context.Background(), "")
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	for _, inv := range got.Invoices {
		if inv.Number == "INV-0000000004" && inv.State != "overdue" {
			t.Errorf("state = %q, want overdue once the due date has passed unpaid", inv.State)
		}
	}
}

func setPaid(t *testing.T, s *Service, req InvoicePaymentRequest) (*InvoicePaymentResult, error) {
	t.Helper()
	return s.SetInvoicePaid(context.Background(), req)
}

// TestARecordedPaymentIsAppendedNotWrittenOver is the contract of the write:
// the other entries survive byte-identical, the payment lands with its note,
// and the commit message names the invoice and the day.
func TestARecordedPaymentIsAppendedNotWrittenOver(t *testing.T) {
	gh := newFakeGitHub(map[string]string{DefaultPaidInvoicesPath: paidInvoicesFixture})
	s := invoiceService(t, gh)

	got, err := setPaid(t, s, InvoicePaymentRequest{
		Invoice: "INV-0000000002", Paid: true, Date: "2026-02-09", Note: "received on the company account",
	})
	if err != nil {
		t.Fatalf("SetInvoicePaid: %v", err)
	}
	if !got.Paid || got.Date != "2026-02-09" || !got.DeployPending || got.SHA == "" {
		t.Errorf("result = %+v", got)
	}

	after := string(gh.files[DefaultPaidInvoicesPath])
	if !strings.Contains(after, `{ "invoice": "INV-0000000001", "date": "2026-02-01" }`) {
		t.Errorf("the earlier payment was reformatted or lost:\n%s", after)
	}
	if !strings.Contains(after, `{ "invoice": "INV-0000000002", "date": "2026-02-09", "note": "received on the company account" }`) {
		t.Errorf("the new payment is not in the file as written:\n%s", after)
	}
	if want := strings.Count(paidInvoicesFixture, "\n"); strings.Count(after, "\n") != want+1 {
		t.Errorf("the file went from %d lines to %d — a whole-file reformat, not an append:\n%s",
			strings.Count(paidInvoicesFixture, "\n"), strings.Count(after, "\n"), after)
	}
	if !strings.Contains(gh.lastMsg, "INV-0000000002") || !strings.Contains(gh.lastMsg, "2026-02-09") {
		t.Errorf("commit message = %q, want the invoice and the date", gh.lastMsg)
	}
}

func TestAPaymentCorrectionReplacesTheDate(t *testing.T) {
	gh := newFakeGitHub(map[string]string{DefaultPaidInvoicesPath: paidInvoicesFixture})
	s := invoiceService(t, gh)

	if _, err := setPaid(t, s, InvoicePaymentRequest{
		Invoice: "INV-0000000001", Paid: true, Date: "2026-02-03", Reason: "corrected date",
	}); err != nil {
		t.Fatalf("SetInvoicePaid: %v", err)
	}
	after := string(gh.files[DefaultPaidInvoicesPath])
	if strings.Count(after, "INV-0000000001") != 1 {
		t.Errorf("%s is listed more than once after a correction:\n%s", "INV-0000000001", after)
	}
	if !strings.Contains(after, `"date": "2026-02-03"`) {
		t.Errorf("the corrected date did not land:\n%s", after)
	}
}

func TestUnmarkingAPaymentRemovesOnlyThatEntry(t *testing.T) {
	gh := newFakeGitHub(map[string]string{DefaultPaidInvoicesPath: paidInvoicesFixture})
	s := invoiceService(t, gh)

	got, err := setPaid(t, s, InvoicePaymentRequest{Invoice: "INV-0000000001", Paid: false, Reason: "the money bounced"})
	if err != nil {
		t.Fatalf("SetInvoicePaid: %v", err)
	}
	if got.Paid || got.Date != "" {
		t.Errorf("result = %+v, want an unpaid result with no date", got)
	}
	after := string(gh.files[DefaultPaidInvoicesPath])
	if strings.Contains(after, "INV-0000000001") {
		t.Errorf("the payment was not removed:\n%s", after)
	}
	if !strings.Contains(after, `"paid": []`) {
		t.Errorf("the list did not become empty:\n%s", after)
	}
	if !strings.Contains(gh.lastMsg, "removed") {
		t.Errorf("commit message = %q, want it to say the record was removed", gh.lastMsg)
	}
}

func TestUnmarkingAnUnpaidInvoiceIsRefused(t *testing.T) {
	gh := newFakeGitHub(map[string]string{DefaultPaidInvoicesPath: paidInvoicesFixture})
	s := invoiceService(t, gh)

	_, err := setPaid(t, s, InvoicePaymentRequest{Invoice: "INV-0000000002", Paid: false})
	if err == nil || !strings.Contains(err.Error(), "not marked paid") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if gh.puts != 0 {
		t.Errorf("a refusal committed a write")
	}
}

func TestAPaymentIsRefusedFor(t *testing.T) {
	for name, req := range map[string]InvoicePaymentRequest{
		"a draft":            {Invoice: "INV-0000000003", Paid: true, Date: "2026-02-09"},
		"a missing invoice":  {Invoice: "INV-0000000099", Paid: true, Date: "2026-02-09"},
		"a malformed number": {Invoice: "INV-1", Paid: true, Date: "2026-02-09"},
		"a missing date":     {Invoice: "INV-0000000002", Paid: true},
		"a future date":      {Invoice: "INV-0000000002", Paid: true, Date: "2026-02-11"},
	} {
		t.Run(name, func(t *testing.T) {
			gh := newFakeGitHub(map[string]string{DefaultPaidInvoicesPath: paidInvoicesFixture})
			s := invoiceService(t, gh)

			_, err := setPaid(t, s, req)
			if err == nil {
				t.Fatalf("SetInvoicePaid(%+v) succeeded, want a refusal", req)
			}
			if gh.puts != 0 {
				t.Errorf("a refusal committed a write")
			}
		})
	}
}

func TestWritingAPaymentNeedsAStore(t *testing.T) {
	s := invoiceService(t, nil)
	_, err := setPaid(t, s, InvoicePaymentRequest{Invoice: "INV-0000000002", Paid: true, Date: "2026-02-09"})
	if err == nil || !strings.Contains(err.Error(), CodeWriteNotConfigured) {
		t.Fatalf("err = %v, want write_not_configured", err)
	}
}

func TestAPaymentOnAMissingFileStartsTheList(t *testing.T) {
	gh := newFakeGitHub(map[string]string{})
	s := invoiceService(t, gh)

	got, err := setPaid(t, s, InvoicePaymentRequest{Invoice: "INV-0000000002", Paid: true, Date: "2026-02-09"})
	if err != nil {
		t.Fatalf("SetInvoicePaid: %v", err)
	}
	after := string(gh.files[DefaultPaidInvoicesPath])
	if !strings.Contains(after, `"paid": [`) || !strings.Contains(after, "INV-0000000002") {
		t.Errorf("the file was not started:\n%s", after)
	}
	if !got.DeployPending {
		t.Errorf("deploy_pending = false after a real write")
	}
}
