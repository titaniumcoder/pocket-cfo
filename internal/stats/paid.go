package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/paidinvoices"
)

// LoadPaid reads paid-invoices.json into invoice number -> payment date.
// A missing file means nothing has been paid yet, not an error — same
// optional-layer convention as accounts.json.
//
// A duplicated invoice number is resolved last-wins rather than rejected:
// this is the request path, where one bad line must not blank the dashboard.
// ValidatePaid is what refuses it, in CI and at the CLI.
func LoadPaid(path string) (map[string]types.SerializableDate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]types.SerializableDate{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var pf paidinvoices.PaidInvoicesJson
	if err := json.Unmarshal(b, &pf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	paid := make(map[string]types.SerializableDate, len(pf.Paid))
	for _, p := range pf.Paid {
		paid[p.Invoice] = p.Date
	}
	return paid, nil
}

// ValidatePaid checks the rules paid-invoices.json's JSON Schema can't
// express: a payment names an invoice that exists, names it exactly once, and
// never names a draft. invoices is the full set from data/invoices.
//
// Every breach is reported rather than just the first, so one validate run
// shows the whole picture instead of one problem per fix-and-rerun cycle.
func ValidatePaid(pf paidinvoices.PaidInvoicesJson, invoices []*invoice.InvoiceJson) error {
	byNumber := make(map[string]*invoice.InvoiceJson, len(invoices))
	for _, inv := range invoices {
		byNumber[inv.Number] = inv
	}

	var problems []error
	seen := make(map[string]bool, len(pf.Paid))
	for _, p := range pf.Paid {
		if seen[p.Invoice] {
			problems = append(problems, fmt.Errorf("%s: listed more than once", p.Invoice))
			continue
		}
		seen[p.Invoice] = true

		inv, ok := byNumber[p.Invoice]
		if !ok {
			problems = append(problems, fmt.Errorf("%s: no such invoice", p.Invoice))
			continue
		}
		if inv.Status == invoice.InvoiceJsonStatusDraft {
			problems = append(problems, fmt.Errorf("%s: is a draft, and a draft is never paid", p.Invoice))
		}
	}
	return errors.Join(problems...)
}
