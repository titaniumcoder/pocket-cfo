package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/usableinvoices"
)

// LoadUsable reads the invoice → income-month overrides. A missing file is
// the ordinary case — no overrides written, every invoice follows the
// default rule — and reads as an empty map.
func LoadUsable(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var uf usableinvoices.UsableInvoicesJson
	if err := json.Unmarshal(b, &uf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	out := make(map[string]string, len(uf.Overrides))
	for _, o := range uf.Overrides {
		if first, dup := out[o.Invoice]; dup {
			return nil, fmt.Errorf("%s: %s is listed twice, usable in %s and in %s — which one is it?",
				path, o.Invoice, first, o.Month)
		}
		out[o.Invoice] = o.Month
	}
	return out, nil
}

// ValidateUsable holds the rules the schema cannot state across files: the
// invoice exists, is not a draft (a draft has no income to place), and the
// month does not precede the invoice's due month.
func ValidateUsable(uf usableinvoices.UsableInvoicesJson, invoices []*invoice.InvoiceJson) error {
	byNumber := make(map[string]*invoice.InvoiceJson, len(invoices))
	for _, inv := range invoices {
		byNumber[inv.Number] = inv
	}

	var problems []error
	seen := make(map[string]bool, len(uf.Overrides))
	for _, o := range uf.Overrides {
		if seen[o.Invoice] {
			problems = append(problems, fmt.Errorf("%s: listed more than once", o.Invoice))
			continue
		}
		seen[o.Invoice] = true

		inv, ok := byNumber[o.Invoice]
		if !ok {
			problems = append(problems, fmt.Errorf("%s: no such invoice", o.Invoice))
			continue
		}
		if inv.Status == invoice.InvoiceJsonStatusDraft {
			problems = append(problems, fmt.Errorf("%s: is a draft, and a draft has no income to place yet", o.Invoice))
		}
		due := inv.DueDate.Time
		if o.Month < due.Format("2006-01") {
			problems = append(problems, fmt.Errorf("%s: income month %s precedes the due month %s — an invoice cannot pay for a month before it was due",
				o.Invoice, o.Month, due.Format("2006-01")))
		}
	}
	return errors.Join(problems...)
}
