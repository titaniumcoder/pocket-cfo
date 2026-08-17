package stats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/paidinvoices"
)

func TestLoadPaid(t *testing.T) {
	t.Run("missing file is not an error", func(t *testing.T) {
		got, err := LoadPaid(filepath.Join(t.TempDir(), "paid-invoices.json"))
		if err != nil {
			t.Fatalf("LoadPaid(missing) = %v, want no error — nothing paid yet is a normal state", err)
		}
		if len(got) != 0 {
			t.Errorf("LoadPaid(missing) = %v, want empty", got)
		}
	})

	t.Run("reads numbers and dates", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, dir, "paid-invoices.json", `{
			"paid": [
				{ "invoice": "INV-0000000001", "date": "2026-01-15" },
				{ "invoice": "INV-0000000002", "date": "2026-01-28", "note": "bank ref 4412" }
			]
		}`)

		got, err := LoadPaid(filepath.Join(dir, "paid-invoices.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("len(got) = %d, want 2", len(got))
		}
		if want := mustDate("2026-01-15"); !got["INV-0000000001"].Equal(want.Time) {
			t.Errorf("INV-0000000001 = %v, want %v", got["INV-0000000001"], want)
		}
		if want := mustDate("2026-01-28"); !got["INV-0000000002"].Equal(want.Time) {
			t.Errorf("INV-0000000002 = %v, want %v", got["INV-0000000002"], want)
		}
	})

	t.Run("malformed file is an error", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, dir, "paid-invoices.json", `{"paid": [{"invoice": "nope", "date": "2026-01-15"}]}`)
		if _, err := LoadPaid(filepath.Join(dir, "paid-invoices.json")); err == nil {
			t.Error("LoadPaid(bad invoice number) = nil error, want the schema pattern to reject it")
		}
	})
}

func TestValidatePaid(t *testing.T) {
	invoices := []*invoice.InvoiceJson{
		{Number: "INV-0000000001", Status: invoice.InvoiceJsonStatusIssued},
		{Number: "INV-0000000002", Status: invoice.InvoiceJsonStatusIssued},
		{Number: "INV-0000000009", Status: invoice.InvoiceJsonStatusDraft},
	}

	payment := func(number string) paidinvoices.Payment {
		return paidinvoices.Payment{Invoice: number, Date: mustDate("2026-01-15")}
	}

	tests := []struct {
		name    string
		paid    []paidinvoices.Payment
		wantErr string // substring; "" means the file is valid
	}{
		{
			name: "empty is valid",
		},
		{
			name: "every number resolves, each once",
			paid: []paidinvoices.Payment{payment("INV-0000000001"), payment("INV-0000000002")},
		},
		{
			name:    "duplicate invoice number",
			paid:    []paidinvoices.Payment{payment("INV-0000000001"), payment("INV-0000000001")},
			wantErr: "listed more than once",
		},
		{
			name:    "number with no matching invoice",
			paid:    []paidinvoices.Payment{payment("INV-0000000404")},
			wantErr: "no such invoice",
		},
		{
			name:    "draft marked paid",
			paid:    []paidinvoices.Payment{payment("INV-0000000009")},
			wantErr: "is a draft",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePaid(paidinvoices.PaidInvoicesJson{Paid: tt.paid}, invoices)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidatePaid() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePaid() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidatePaid() = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}

	t.Run("reports every breach, not just the first", func(t *testing.T) {
		err := ValidatePaid(paidinvoices.PaidInvoicesJson{Paid: []paidinvoices.Payment{
			payment("INV-0000000404"),
			payment("INV-0000000009"),
		}}, invoices)
		if err == nil {
			t.Fatal("ValidatePaid() = nil, want two errors")
		}
		for _, want := range []string{"no such invoice", "is a draft"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ValidatePaid() = %q, want it to contain %q", err, want)
			}
		}
	})
}

func TestLoadPaidRefusesADuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "paid-invoices.json")
	body := `{"paid":[
		{"invoice":"INV-0000000001","date":"2026-02-01"},
		{"invoice":"INV-0000000001","date":"2026-03-15"}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPaid(path)
	if err == nil {
		t.Fatal("want an error for an invoice listed twice")
	}
	for _, want := range []string{"INV-0000000001", "2026-02-01", "2026-03-15"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}
