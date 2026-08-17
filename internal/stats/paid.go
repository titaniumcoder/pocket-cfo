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
		if first, dup := paid[p.Invoice]; dup {
			return nil, fmt.Errorf("%s: %s is listed twice, paid on %s and on %s — which one is it?",
				path, p.Invoice, first.Format("2006-01-02"), p.Date.Format("2006-01-02"))
		}
		paid[p.Invoice] = p.Date
	}
	return paid, nil
}

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
