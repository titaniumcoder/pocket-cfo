package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/recipient"
)

func mustDate(s string) types.SerializableDate {
	var d types.SerializableDate
	if err := d.UnmarshalJSON([]byte(`"` + s + `"`)); err != nil {
		panic(err)
	}
	return d
}

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInvoices_IncludesDraftsExcludesAnnulled(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "issued.json", `{
		"schema_version": 1, "number": "INV-0000000001", "status": "issued", "type": "invoice",
		"title": "t", "issue_date": "2026-01-01", "due_date": "2026-01-08", "currency": "EUR", "language": "de",
		"issuer": {"legal_name":"x","address":{"line1":"a","postal_code":"1","city":"c","country_code":"BG"},"tax_id":"1","vat_id":"BG1","bank":{"name":"b","iban":"i","bic":"b"},"default_currency":"EUR"},
		"recipient": {"number":1,"legal_name":"r","address":{"line1":"a","postal_code":"1","city":"c","country_code":"BG"},"is_business":true,"language":"de","payment_terms_days":30,"email":"e@x.com"},
		"lines": [{"description":{"de":"d","bg":"d"},"unit_price":1000,"vat_rate":0}],
		"tax": {"regime":"domestic_standard","citations":[],"note":{"de":"n","bg":"n"}},
		"paid": null, "annulment": null
	}`)
	writeFixture(t, dir, "draft.json", `{
		"schema_version": 1, "number": "INV-0000000002", "status": "draft", "type": "invoice",
		"title": "t", "issue_date": "2026-01-01", "due_date": "2026-01-08", "currency": "EUR", "language": "de",
		"issuer": {"legal_name":"x","address":{"line1":"a","postal_code":"1","city":"c","country_code":"BG"},"tax_id":"1","vat_id":"BG1","bank":{"name":"b","iban":"i","bic":"b"},"default_currency":"EUR"},
		"recipient": {"number":1,"legal_name":"r","address":{"line1":"a","postal_code":"1","city":"c","country_code":"BG"},"is_business":true,"language":"de","payment_terms_days":30,"email":"e@x.com"},
		"lines": [{"description":{"de":"d","bg":"d"},"unit_price":1000,"vat_rate":0}],
		"tax": {"regime":"domestic_standard","citations":[],"note":{"de":"n","bg":"n"}},
		"paid": null, "annulment": null
	}`)
	writeFixture(t, dir, "annulled.json", `{
		"schema_version": 1, "number": "INV-0000000003", "status": "issued", "type": "invoice",
		"title": "t", "issue_date": "2026-01-01", "due_date": "2026-01-08", "currency": "EUR", "language": "de",
		"issuer": {"legal_name":"x","address":{"line1":"a","postal_code":"1","city":"c","country_code":"BG"},"tax_id":"1","vat_id":"BG1","bank":{"name":"b","iban":"i","bic":"b"},"default_currency":"EUR"},
		"recipient": {"number":1,"legal_name":"r","address":{"line1":"a","postal_code":"1","city":"c","country_code":"BG"},"is_business":true,"language":"de","payment_terms_days":30,"email":"e@x.com"},
		"lines": [{"description":{"de":"d","bg":"d"},"unit_price":1000,"vat_rate":0}],
		"tax": {"regime":"domestic_standard","citations":[],"note":{"de":"n","bg":"n"}},
		"paid": null, "annulment": {"date":"2026-01-02","reason_de":"r","reason_bg":"r"}
	}`)

	got, err := LoadInvoices(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (issued + draft, annulled excluded)", len(got))
	}
	numbers := map[string]bool{}
	for _, inv := range got {
		numbers[inv.Number] = true
	}
	if !numbers["INV-0000000001"] {
		t.Errorf("got %v, want the issued invoice included", numbers)
	}
	if !numbers["INV-0000000002"] {
		t.Errorf("got %v, want the draft invoice included", numbers)
	}
	if numbers["INV-0000000003"] {
		t.Errorf("got %v, want the annulled invoice excluded", numbers)
	}
}

func strp(v string) *string { return &v }

func line(unitPrice int) invoice.Line {
	return invoice.Line{
		Description: invoice.LocalizedString{De: strp("d"), Bg: strp("d")},
		UnitPrice:   unitPrice,
		VatRate:     0,
	}
}

func TestAggregate(t *testing.T) {
	recipients := []recipient.RecipientJson{
		{Number: 1, LegalName: "Alice Ltd", Email: "alice@example.com", Address: recipient.Address{
			Line1: "Street 1", PostalCode: "1000", City: "Sofia", CountryCode: "BG",
		}},
		{Number: 2, LegalName: "Bob GmbH", Email: "bob@example.com", Address: recipient.Address{
			Line1: "Street 2", PostalCode: "2000", City: "Graz", CountryCode: "AT",
		}},
	}

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	paidDate := mustDate("2025-03-01")
	invoices := []*invoice.InvoiceJson{
		{
			Number: "INV-A1", Title: "A1", Status: invoice.InvoiceJsonStatusIssued,
			IssueDate: mustDate("2025-02-01"), DueDate: mustDate("2025-02-08"),
			Recipient: invoice.RecipientSnapshot{Number: 1, LegalName: "Alice Ltd"},
			Lines:     []invoice.Line{line(100000)}, // 1000.00
			Paid:      &paidDate,
		},
		{
			Number: "INV-A2", Title: "A2", Status: invoice.InvoiceJsonStatusIssued,
			IssueDate: mustDate("2026-05-01"), DueDate: mustDate("2026-05-08"), // before now → overdue
			Recipient: invoice.RecipientSnapshot{Number: 1, LegalName: "Alice Ltd"},
			Lines:     []invoice.Line{line(50000)}, // 500.00, unpaid
		},
		{
			Number: "INV-A3", Title: "A3", Status: invoice.InvoiceJsonStatusIssued,
			IssueDate: mustDate("2026-06-01"), DueDate: mustDate("2026-07-01"), // after now → issued (open)
			Recipient: invoice.RecipientSnapshot{Number: 1, LegalName: "Alice Ltd"},
			Lines:     []invoice.Line{line(20000)}, // 200.00, unpaid
		},
		{
			Number: "INV-A4", Title: "A4", Status: invoice.InvoiceJsonStatusDraft,
			IssueDate: mustDate("2026-06-10"), DueDate: mustDate("2026-06-20"),
			Recipient: invoice.RecipientSnapshot{Number: 1, LegalName: "Alice Ltd"},
			Lines:     []invoice.Line{line(30000)}, // 300.00 — must not count toward any ledger total
		},
		// Bob (recipient 2) has no invoices at all — must not appear in recipientRows.
	}

	states := map[string]string{}

	years, recipientRows, invoiceRows, err := Aggregate(invoices, recipients, nil, now)
	if err != nil {
		t.Fatal(err)
	}

	if want := []int{2026, 2025}; !equalInts(years, want) {
		t.Errorf("years = %v, want %v", years, want)
	}
	if len(recipientRows) != 1 {
		t.Fatalf("len(recipientRows) = %d, want 1 (Bob has no invoices)", len(recipientRows))
	}
	alice := recipientRows[0]
	if alice.LegalName != "Alice Ltd" {
		t.Fatalf("recipientRows[0] = %+v, want Alice Ltd", alice)
	}
	if alice.TotalInFrame != 170000 {
		t.Errorf("TotalInFrame (all years) = %d, want 170000 (draft's 30000 excluded)", alice.TotalInFrame)
	}
	if alice.Outstanding != 70000 {
		t.Errorf("Outstanding = %d, want 70000 (A2 + A3, draft excluded)", alice.Outstanding)
	}
	if len(invoiceRows) != 4 {
		t.Fatalf("len(invoiceRows) = %d, want 4 (draft included in the list)", len(invoiceRows))
	}
	for _, row := range invoiceRows {
		states[row.Number] = row.State
	}
	wantStates := map[string]string{"INV-A1": "paid", "INV-A2": "overdue", "INV-A3": "issued", "INV-A4": "draft"}
	for number, want := range wantStates {
		if got := states[number]; got != want {
			t.Errorf("state[%s] = %q, want %q", number, got, want)
		}
	}

	year2025 := 2025
	_, recipientRows2025, invoiceRows2025, err := Aggregate(invoices, recipients, &year2025, now)
	if err != nil {
		t.Fatal(err)
	}
	if recipientRows2025[0].TotalInFrame != 100000 {
		t.Errorf("TotalInFrame (2025) = %d, want 100000", recipientRows2025[0].TotalInFrame)
	}
	if recipientRows2025[0].Outstanding != 70000 {
		t.Errorf("Outstanding must stay 70000 regardless of year filter, got %d", recipientRows2025[0].Outstanding)
	}
	if len(invoiceRows2025) != 1 || invoiceRows2025[0].Number != "INV-A1" {
		t.Errorf("invoiceRows(2025) = %+v, want just INV-A1", invoiceRows2025)
	}
}

// TestAggregate_DraftOnlyRecipientShownWithZeroedLedger pins the actual
// business rule: a recipient is shown when they have activity in the
// selected year, and a draft counts as activity even though it isn't money
// yet. Only the ledger totals (TotalInFrame, Outstanding) stay restricted to
// issued invoices — a recipient whose only invoice ever is a draft must
// still appear, just with a zeroed row.
func TestAggregate_DraftOnlyRecipientShownWithZeroedLedger(t *testing.T) {
	recipients := []recipient.RecipientJson{
		{Number: 1, LegalName: "Carol Inc", Email: "carol@example.com", Address: recipient.Address{
			Line1: "Street 3", PostalCode: "3000", City: "Plovdiv", CountryCode: "BG",
		}},
	}
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	invoices := []*invoice.InvoiceJson{
		{
			Number: "INV-C1", Title: "C1", Status: invoice.InvoiceJsonStatusDraft,
			IssueDate: mustDate("2026-06-10"), DueDate: mustDate("2026-06-20"),
			Recipient: invoice.RecipientSnapshot{Number: 1, LegalName: "Carol Inc"},
			Lines:     []invoice.Line{line(30000)},
		},
	}

	_, recipientRows, _, err := Aggregate(invoices, recipients, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipientRows) != 1 {
		t.Fatalf("len(recipientRows) = %d, want 1 (a draft is activity, Carol must be shown)", len(recipientRows))
	}
	carol := recipientRows[0]
	if carol.TotalInFrame != 0 {
		t.Errorf("TotalInFrame = %d, want 0 (draft isn't a receivable)", carol.TotalInFrame)
	}
	if carol.Outstanding != 0 {
		t.Errorf("Outstanding = %d, want 0 (draft isn't a receivable)", carol.Outstanding)
	}

	// Filtering to a year with no activity at all excludes her again.
	otherYear := 2025
	_, recipientRows2025, _, err := Aggregate(invoices, recipients, &otherYear, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipientRows2025) != 0 {
		t.Errorf("recipientRows(2025) = %+v, want empty (no activity in 2025)", recipientRows2025)
	}
}

func TestAggregate_RecipientWithNoInvoicesAtAllNotShown(t *testing.T) {
	recipients := []recipient.RecipientJson{
		{Number: 1, LegalName: "Dave LLC", Email: "dave@example.com", Address: recipient.Address{
			Line1: "Street 4", PostalCode: "4000", City: "Burgas", CountryCode: "BG",
		}},
	}
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	_, recipientRows, _, err := Aggregate(nil, recipients, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipientRows) != 0 {
		t.Errorf("recipientRows = %+v, want empty (no invoices at all, ever)", recipientRows)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
