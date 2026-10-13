package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/usableinvoices"
)

func TestLoadUsableMissingFileIsNoOverrides(t *testing.T) {
	got, err := LoadUsable(filepath.Join(t.TempDir(), "usable-invoices.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("overrides = %+v, want an empty map", got)
	}
}

func TestLoadUsableRefusesADuplicateEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usable-invoices.json")
	body := `{"overrides":[
		{"invoice":"INV-0000000001","month":"2026-08"},
		{"invoice":"INV-0000000001","month":"2026-09"}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUsable(path); err == nil {
		t.Fatal("the same invoice listed twice was accepted")
	}
}

func TestValidateUsable(t *testing.T) {
	issued := &invoice.InvoiceJson{Number: "INV-0000000001", Status: invoice.InvoiceJsonStatusIssued,
		DueDate: dateOf(2026, 1, 19)}
	draft := &invoice.InvoiceJson{Number: "INV-0000000002", Status: invoice.InvoiceJsonStatusDraft,
		DueDate: dateOf(2026, 1, 19)}
	early := &invoice.InvoiceJson{Number: "INV-0000000004", Status: invoice.InvoiceJsonStatusIssued,
		DueDate: dateOf(2026, 2, 19)}

	uf := usableinvoices.UsableInvoicesJson{Overrides: []usableinvoices.Override{
		{Invoice: "INV-0000000002", Month: "2026-02"},
		{Invoice: "INV-0000000003", Month: "2026-02"},
		{Invoice: "INV-0000000004", Month: "2025-12"},
		{Invoice: "INV-0000000004", Month: "2026-03"},
	}}
	err := ValidateUsable(uf, []*invoice.InvoiceJson{issued, draft, early})
	if err == nil {
		t.Fatal("a draft target, an unknown invoice, a month before the due month and a duplicate were all accepted")
	}
	for _, want := range []string{"precede", "draft", "no such invoice", "more than once"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func dateOf(y int, m time.Month, d int) types.SerializableDate {
	return types.SerializableDate{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}
