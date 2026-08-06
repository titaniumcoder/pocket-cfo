package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/translate"
)

func strp(v string) *string { return &v }

// fakeDeepLTransport records every text it was asked to translate, so tests
// can assert the tax note was never sent to DeepL even when it's
// "incomplete" by the new schema's standards.
type fakeDeepLTransport struct {
	t        *testing.T
	requests []string
}

func (f *fakeDeepLTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	decoded, _ := url.QueryUnescape(string(body))
	f.requests = append(f.requests, decoded)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"translations":[{"text":"translated"}]}`)),
	}, nil
}

func draftInvoiceFixture(t *testing.T, number string, description, discountLabel *string) invoice.InvoiceJson {
	t.Helper()
	inv := invoice.InvoiceJson{
		SchemaVersion: 1, Number: number, Status: invoice.InvoiceJsonStatusDraft,
		Type: "invoice", Title: "Test", IssueDate: mustDate("2026-01-01"), DueDate: mustDate("2026-01-15"),
		Currency: "EUR", Language: invoice.InvoiceJsonLanguageDe,
		Issuer: invoice.IssuerSnapshot{
			LegalName: "Issuer", Address: invoice.Address{Line1: "S1", PostalCode: "1000", City: "Sofia", CountryCode: "BG"},
			TaxId: "1", VatId: "BG1", Bank: invoice.Bank{Name: "B", Iban: "I", Bic: "B"}, DefaultCurrency: "EUR",
		},
		Recipient: invoice.RecipientSnapshot{
			Number: 1, LegalName: "R", IsBusiness: true, Language: invoice.RecipientSnapshotLanguageDe,
			PaymentTermsDays: 14, Email: "r@example.com",
			Address: invoice.Address{Line1: "S2", PostalCode: "2000", City: "Vienna", CountryCode: "AT"},
		},
		Lines: []invoice.Line{{Description: invoice.LocalizedString{De: description}, UnitPrice: 100, VatRate: 0}},
		Tax: invoice.Tax{
			Regime: invoice.TaxRegimeOutsideEuPlaceOfSupply, Citations: []string{"c"},
			Note: invoice.LocalizedString{De: strp("Hinweis")}, // bg deliberately missing too
		},
	}
	if discountLabel != nil {
		percent := 100
		inv.Discounts = []invoice.Discount{{Label: invoice.LocalizedString{De: discountLabel}, Percent: &percent}}
	}
	return inv
}

func writeInvoiceFixture(t *testing.T, dir string, inv invoice.InvoiceJson) string {
	t.Helper()
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, inv.Number+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTranslateOne_FillsMissingBgOnDraft(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(invoicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inv := draftInvoiceFixture(t, "INV-0000000001", strp("Arbeit"), strp("Rabatt"))
	path := writeInvoiceFixture(t, invoicesDir, inv)

	transport := &fakeDeepLTransport{t: t}
	client := &translate.Client{APIKey: "test-key:fx", HTTPClient: &http.Client{Transport: transport}}

	changed, err := translateOne(context.Background(), client, path)
	if err != nil {
		t.Fatalf("translateOne: %v", err)
	}
	if !changed {
		t.Fatal("expected translateOne to report a change")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got invoice.InvoiceJson
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Lines[0].Description.Bg == nil || *got.Lines[0].Description.Bg == "" {
		t.Error("line description bg was not filled")
	}
	if got.Discounts[0].Label.Bg == nil || *got.Discounts[0].Label.Bg == "" {
		t.Error("discount label bg was not filled")
	}

	// The critical guard: the tax note is deliberately incomplete (no bg)
	// in this fixture, and must never be sent to DeepL or filled in.
	if got.Tax.Note.Bg != nil {
		t.Error("tax note bg must never be auto-filled — it's catalog-sourced/human-authored, never machine-translated")
	}
	for _, req := range transport.requests {
		if strings.Contains(req, "Hinweis") {
			t.Errorf("tax note text was sent to DeepL: %v", transport.requests)
		}
	}
}

func TestTranslateOne_IssuedInvoiceUntouched(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(invoicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inv := draftInvoiceFixture(t, "INV-0000000002", strp("Arbeit"), nil)
	inv.Status = invoice.InvoiceJsonStatusIssued
	path := writeInvoiceFixture(t, invoicesDir, inv)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	transport := &fakeDeepLTransport{t: t}
	client := &translate.Client{APIKey: "test-key:fx", HTTPClient: &http.Client{Transport: transport}}

	changed, err := translateOne(context.Background(), client, path)
	if err != nil {
		t.Fatalf("translateOne: %v", err)
	}
	if changed {
		t.Error("expected no change for an issued invoice")
	}
	if len(transport.requests) != 0 {
		t.Errorf("issued invoice must never be sent to DeepL, got %d requests", len(transport.requests))
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("issued invoice file was modified")
	}
}
