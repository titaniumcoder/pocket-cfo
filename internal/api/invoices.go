package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
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
	Number     string `json:"number"`
	Title      string `json:"title"`
	Recipient  string `json:"recipient"`
	IssueDate  string `json:"issue_date"`
	DueDate    string `json:"due_date"`
	TotalCents int64  `json:"total_cents"`
	State      string `json:"state"`
	PaidOn     string `json:"paid_on,omitempty"`
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
		out.Invoices = append(out.Invoices, inv)
	}
	return out, nil
}

type InvoicePaymentRequest struct {
	Invoice string `json:"invoice"`
	Paid    bool   `json:"paid"`
	Date    string `json:"date,omitempty"`
	Note    string `json:"note,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type InvoicePaymentResult struct {
	Invoice       string `json:"invoice"`
	Paid          bool   `json:"paid"`
	Date          string `json:"date,omitempty"`
	SHA           string `json:"sha"`
	DeployPending bool   `json:"deploy_pending"`
}

func (s *Service) SetInvoicePaid(ctx context.Context, req InvoicePaymentRequest) (*InvoicePaymentResult, error) {
	number := strings.TrimSpace(req.Invoice)
	if !invoiceNumberRE.MatchString(number) {
		return nil, errorf(CodeInvalidRequest, "invoice %q must look like INV-0000000001", req.Invoice)
	}
	paidOn := ""
	if req.Paid {
		if req.Date == "" {
			return nil, errorf(CodeInvalidRequest, "date is required when paid is true — a payment is a fact with a day")
		}
		day, err := time.Parse("2006-01-02", req.Date)
		if err != nil {
			return nil, errorf(CodeInvalidRequest, "date %q is not a real date", req.Date)
		}
		if day.After(s.now()) {
			return nil, errorf(CodeInvalidRequest, "date %s is in the future — a payment is read off the bank, never projected", req.Date)
		}
		paidOn = req.Date
	}
	if s.Store == nil {
		return nil, errorf(CodeWriteNotConfigured, "writes are not configured")
	}

	invoices, err := stats.LoadInvoices(s.invoicesDir())
	if err != nil {
		return nil, errorf(CodeInternal, "reading invoices: %v", err)
	}
	target := findInvoice(invoices, number)
	if target == nil {
		return nil, errorf(CodeNotFound, "no invoice %s — spell it as list_invoices spells it", number)
	}
	if target.Status == invoice.InvoiceJsonStatusDraft {
		return nil, errorf(CodeInvalidRequest, "%s is a draft, and a draft is never paid — issue it first", number)
	}

	path := s.paidInvoicesPath()
	src, sha, err := s.Store.Get(ctx, path)
	if err != nil && !errors.Is(err, ErrNotFound) {
		if e, ok := err.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "reading %s: %v", path, err)
	}

	var pf paidinvoices.PaidInvoicesJson
	if src != nil {
		if uerr := json.Unmarshal(src, &pf); uerr != nil {
			return nil, errorf(CodeUpstream, "the committed %s does not parse: %v", path, uerr)
		}
	}

	updated, changed, err := applyPayment(pf, number, req, paidOn)
	if err != nil {
		return nil, err
	}
	if !changed {
		return &InvoicePaymentResult{
			Invoice: number, Paid: req.Paid, Date: paidOn,
			SHA: sha, DeployPending: false,
		}, nil
	}

	out, err := marshalPaid(updated, pf.Schema)
	if err != nil {
		return nil, err
	}
	if verr := verifyOnlyThisPaymentChanged(src, out, invoices, number, req.Paid, paidOn); verr != nil {
		return nil, verr
	}

	newSHA, perr := s.Store.Put(ctx, path, out, sha, paidCommitMessage(number, req, paidOn))
	if perr != nil {
		if e, ok := perr.(*Error); ok {
			return nil, e
		}
		return nil, errorf(CodeUpstream, "writing %s: %v", path, perr)
	}
	return &InvoicePaymentResult{
		Invoice: number, Paid: req.Paid, Date: paidOn,
		SHA: newSHA, DeployPending: true,
	}, nil
}

func applyPayment(pf paidinvoices.PaidInvoicesJson, number string, req InvoicePaymentRequest, paidOn string) ([]paidinvoices.Payment, bool, error) {
	out := make([]paidinvoices.Payment, 0, len(pf.Paid)+1)
	listed, changed := false, false
	for _, p := range pf.Paid {
		if p.Invoice == number {
			listed = true
			if !req.Paid {
				changed = true
				continue
			}
			if samePayment(p, req, paidOn) {
				out = append(out, p)
				continue
			}
			changed = true
			out = append(out, paymentFor(number, req, paidOn))
			continue
		}
		out = append(out, p)
	}
	if req.Paid && !listed {
		out = append(out, paymentFor(number, req, paidOn))
		changed = true
	}
	return out, changed, nil
}

func sameNote(note *string, sent string) bool {
	held := ""
	if note != nil {
		held = strings.TrimSpace(*note)
	}
	return held == strings.TrimSpace(sent)
}

func samePayment(p paidinvoices.Payment, req InvoicePaymentRequest, paidOn string) bool {
	return p.Date.Format("2006-01-02") == paidOn && sameNote(p.Note, req.Note)
}

func paymentFor(number string, req InvoicePaymentRequest, paidOn string) paidinvoices.Payment {
	p := paidinvoices.Payment{Invoice: number, Date: types.SerializableDate{Time: mustDate(paidOn)}}
	if note := strings.TrimSpace(req.Note); note != "" {
		p.Note = &note
	}
	return p
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

func marshalPaid(payments []paidinvoices.Payment, schema *string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("{\n")
	name := "../schemas/paid-invoices.json"
	if schema != nil && *schema != "" {
		name = *schema
	}
	buf.WriteString(`  "$schema": ` + asJSON(name) + ",\n")
	if len(payments) == 0 {
		buf.WriteString(`  "paid": []` + "\n}\n")
		return buf.Bytes(), nil
	}
	buf.WriteString(`  "paid": [` + "\n")
	for i, p := range payments {
		line := `    { "invoice": ` + asJSON(p.Invoice) + `, "date": ` + asJSON(p.Date.Format("2006-01-02"))
		if p.Note != nil && *p.Note != "" {
			line += `, "note": ` + asJSON(*p.Note)
		}
		line += " }"
		if i < len(payments)-1 {
			line += ","
		}
		buf.WriteString(line + "\n")
	}
	buf.WriteString("  ]\n}\n")
	out := buf.Bytes()

	var check paidinvoices.PaidInvoicesJson
	if err := json.Unmarshal(out, &check); err != nil {
		return nil, errorf(CodeInternal, "the result does not satisfy paid-invoices.json's schema: %v", err)
	}
	return out, nil
}

func verifyOnlyThisPaymentChanged(before, after []byte, invoices []*invoice.InvoiceJson, number string, paid bool, paidOn string) error {
	var pf paidinvoices.PaidInvoicesJson
	if err := json.Unmarshal(after, &pf); err != nil {
		return errorf(CodeInternal, "the result does not satisfy paid-invoices.json's schema: %v", err)
	}
	if err := stats.ValidatePaid(pf, invoices); err != nil {
		return errorf(CodeValidationFailed, "the result fails validation: %v", err)
	}
	found := false
	for _, p := range pf.Paid {
		if p.Invoice != number {
			continue
		}
		found = true
		if !paid || p.Date.Format("2006-01-02") != paidOn {
			return errorf(CodeInternal, "the result does not carry the payment that was sent")
		}
	}
	if paid != found {
		return errorf(CodeInternal, "the result does not carry the payment that was sent")
	}
	var a, b any
	if before != nil {
		if err := json.Unmarshal(before, &a); err != nil {
			return errorf(CodeUpstream, "paid-invoices.json does not parse: %v", err)
		}
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return errorf(CodeInternal, "the result does not parse: %v", err)
	}
	if !equalJSON(withoutThePayment(a, number), withoutThePayment(b, number)) {
		return errorf(CodeInternal, "the result differs from the original by more than this one payment")
	}
	return nil
}

func withoutThePayment(doc any, number string) any {
	root, _ := doc.(map[string]any)
	if root == nil {
		return map[string]any{"paid": []any{}}
	}
	paid, ok := root["paid"].([]any)
	if !ok {
		paid = []any{}
	}
	out := make([]any, 0, len(paid))
	for _, raw := range paid {
		if p, ok := raw.(map[string]any); ok && p["invoice"] == number {
			continue
		}
		out = append(out, raw)
	}
	return map[string]any{"paid": out}
}

func equalJSON(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	return aerr == nil && berr == nil && bytes.Equal(ab, bb)
}

func paidCommitMessage(number string, req InvoicePaymentRequest, paidOn string) string {
	verb := "paid on " + paidOn
	if !req.Paid {
		verb = "payment record removed"
	}
	body := strings.TrimSpace(req.Reason)
	if body == "" {
		body = strings.TrimSpace(req.Note)
	}
	if body != "" {
		body = "\n\n" + body
	}
	return fmt.Sprintf("feat(invoices): %s %s%s\n", number, verb, body)
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
