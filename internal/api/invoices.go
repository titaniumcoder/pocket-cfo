package api

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/atombender/go-jsonschema/pkg/types"

	"github.com/titaniumcoder/pocket-cfo/internal/schema/invoice"
	"github.com/titaniumcoder/pocket-cfo/internal/schema/paidinvoices"
	"github.com/titaniumcoder/pocket-cfo/internal/stats"
)

const DefaultInvoicesDir = "data/invoices"
const DefaultPaidInvoicesPath = "data/paid-invoices.json"

var invoiceNumberRE = regexp.MustCompile(`^INV-\d{10}$`)

type Invoice struct {
	Number     string   `json:"number"`
	Title      string   `json:"title"`
	Recipient  string   `json:"recipient"`
	IssueDate  string   `json:"issue_date"`
	DueDate    string   `json:"due_date"`
	TotalCents int64    `json:"total_cents"`
	State      string   `json:"state"`
	PaidOn     string   `json:"paid_on,omitempty"`
	PDFs       []string `json:"pdfs"`
}

type InvoiceList struct {
	Years    []int     `json:"years"`
	Invoices []Invoice `json:"invoices"`
}

func (s *Service) Invoices(ctx context.Context, year string) (*InvoiceList, error) {
	var y *int
	if year != "" {
		parsed, err := strconv.Atoi(year)
		if err != nil || parsed < minYear || parsed > maxYear {
			return nil, errorf(CodeInvalidRequest, "year %q must look like 2026", year)
		}
		y = &parsed
	}
	invoices, err := stats.LoadInvoices(s.invoicesDir())
	if err != nil {
		return nil, errorf(CodeInternal, "reading invoices: %v", err)
	}
	paid, err := s.paidMap(ctx)
	if err != nil {
		return nil, err
	}
	years, _, rows, err := stats.Aggregate(invoices, nil, paid, y, s.now())
	if err != nil {
		return nil, errorf(CodeInternal, "aggregating invoices: %v", err)
	}
	out := &InvoiceList{Years: years, Invoices: make([]Invoice, 0, len(rows))}
	for _, r := range rows {
		inv := Invoice{
			Number: r.Number, Title: r.Title, Recipient: r.RecipientName,
			IssueDate: r.IssueDate.Format("2006-01-02"), DueDate: r.DueDate.Format("2006-01-02"),
			TotalCents: r.GrandTotal, State: r.State,
		}
		if r.Paid != nil {
			inv.PaidOn = r.Paid.Format("2006-01-02")
		}
		inv.PDFs = pdfVariantsFor(r.State)
		out.Invoices = append(out.Invoices, inv)
	}
	return out, nil
}

func pdfVariantsFor(state string) []string {
	switch state {
	case "draft":
		return []string{"draft"}
	case "paid":
		return []string{"original", "paid"}
	default:
		return []string{"original"}
	}
}

func mustDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func findInvoice(invoices []*invoice.InvoiceJson, number string) *invoice.InvoiceJson {
	for _, inv := range invoices {
		if inv.Number == number {
			return inv
		}
	}
	return nil
}

func (s *Service) paidMap(ctx context.Context) (map[string]types.SerializableDate, error) {
	path := s.paidInvoicesPath()
	if s.Store == nil {
		paid, err := stats.LoadPaid(path)
		if err != nil {
			return nil, errorf(CodeInternal, "%v", err)
		}
		return paid, nil
	}
	raw, _, err := s.Store.Get(ctx, path)
	if errors.Is(err, ErrNotFound) {
		return map[string]types.SerializableDate{}, nil
	}
	if e, ok := err.(*Error); ok {
		return nil, e
	}
	if err != nil {
		return nil, errorf(CodeUpstream, "reading %s: %v", path, err)
	}
	var pf paidinvoices.PaidInvoicesJson
	if uerr := json.Unmarshal(raw, &pf); uerr != nil {
		return nil, errorf(CodeUpstream, "the committed %s does not parse: %v", path, uerr)
	}
	paid := make(map[string]types.SerializableDate, len(pf.Paid))
	for _, p := range pf.Paid {
		if _, dup := paid[p.Invoice]; dup {
			return nil, errorf(CodeUpstream, "%s: %s is listed more than once — which payment is it?", path, p.Invoice)
		}
		paid[p.Invoice] = p.Date
	}
	return paid, nil
}

func (s *Service) invoicesDir() string {
	if s.InvoicesDir != "" {
		return s.InvoicesDir
	}
	return DefaultInvoicesDir
}

func (s *Service) paidInvoicesPath() string {
	if s.PaidInvoicesPath != "" {
		return s.PaidInvoicesPath
	}
	return DefaultPaidInvoicesPath
}
